package kernel

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// The record is what an agent reads about an item beyond its definition: the
// newest version of each attachment and the item's children. Before a run on
// this machine it is written to a read-only folder under the working tree
// (.satelle/runs/, gitignored) and named in the prompt, with small bodies also
// inlined within these bounds. A detached run gets the inline part only.
const (
	InlineBodyMax     = 16 << 10
	InlineTotalMax    = 64 << 10
	InlineChildrenMax = 200
	RejectNotesMax    = 8 << 10
	// RecordLead starts the line naming the record folder in a prompt.
	RecordLead = "The item's record is in the read-only folder "
	runsDir    = "runs"
)

// Record is one run's record: its folder ("" when none was written) and the
// prompt section describing it.
type Record struct {
	Dir     string
	Section string
}

// Close removes the record folder, restoring write permission first.
func (r Record) Close() error {
	if r.Dir == "" {
		return nil
	}
	_ = filepath.WalkDir(r.Dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	return os.RemoveAll(r.Dir)
}

var hexSHA = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// resolveAttachment picks the version of name: the newest with no sha, else
// the one whose sha256 is sha or starts with it (at least 7 hex characters).
func resolveAttachment(atts []item.Attachment, name, sha string) (item.Attachment, error) {
	var versions []item.Attachment
	for _, a := range atts {
		if a.Name == name {
			versions = append(versions, a)
		}
	}
	if len(versions) == 0 {
		return item.Attachment{}, fmt.Errorf("no attachment %q", name)
	}
	if sha == "" {
		return versions[len(versions)-1], nil
	}
	sha = strings.ToLower(sha)
	if !hexSHA.MatchString(sha) {
		return item.Attachment{}, fmt.Errorf("sha %q is not a sha256 or a hex prefix of at least 7 characters", sha)
	}
	matches := map[string]item.Attachment{}
	for _, a := range versions {
		if strings.HasPrefix(a.SHA256, sha) {
			matches[a.SHA256] = a
		}
	}
	switch len(matches) {
	case 0:
		return item.Attachment{}, fmt.Errorf("attachment %q has no version %s", name, sha)
	case 1:
		for _, a := range matches {
			return a, nil
		}
	}
	return item.Attachment{}, fmt.Errorf("sha prefix %s is ambiguous for attachment %q", sha, name)
}

// AttachmentBody returns a version of an item's attachment (the newest with
// sha "") and its body.
func (k *Kernel) AttachmentBody(id, name, sha string) (item.Attachment, []byte, error) {
	_, events, err := k.load(id)
	if err != nil {
		return item.Attachment{}, nil, err
	}
	a, err := resolveAttachment(item.FoldAttachments(events), name, sha)
	if err != nil {
		return item.Attachment{}, nil, fmt.Errorf("%s: %w", id, err)
	}
	body, err := k.Ledger.GetBlob(a.SHA256)
	if err != nil {
		return a, nil, fmt.Errorf("%s: attachment %q body %s: %w", id, name, a.SHA256, err)
	}
	return a, body, nil
}

// OpenRecord writes the record folder of item id and returns it; the caller
// closes it after the run.
func (k *Kernel) OpenRecord(id string) (Record, error) {
	it, events, err := k.load(id)
	if err != nil {
		return Record{}, err
	}
	return k.record(it, events, true)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// fileName is an attachment name made safe as one path element.
func fileName(name string) string {
	f := unsafeName.ReplaceAllString(name, "_")
	if f == "" || f == "." || f == ".." {
		return "_"
	}
	return f
}

// record builds the item's record. With folder it writes the read-only folder
// under <Root>/runs and the section names its paths; without, the section is
// the inline part only and names no local path. An item with no attachments
// and no children has an empty record.
func (k *Kernel) record(it item.Item, events []ledger.Event, folder bool) (Record, error) {
	atts, _ := item.LatestAttachments(item.FoldAttachments(events))
	children, err := k.List(cache.Query{Parent: it.ID})
	if err != nil {
		return Record{}, err
	}
	if len(atts) == 0 && len(children) == 0 {
		return Record{}, nil
	}
	bodies := make([][]byte, len(atts))
	for i, a := range atts {
		if bodies[i], err = k.Ledger.GetBlob(a.SHA256); err != nil {
			return Record{}, fmt.Errorf("%s: attachment %q body %s: %w", it.ID, a.Name, a.SHA256, err)
		}
	}
	var rec Record
	paths := make([]string, len(atts))
	childrenPath := ""
	if folder {
		if rec.Dir, err = k.writeRecord(it, atts, bodies, children, paths); err != nil {
			return Record{}, err
		}
		childrenPath = filepath.Join(rec.Dir, "children.md")
	}
	rec.Section = renderRecord(atts, bodies, paths, children, rec.Dir, childrenPath)
	return rec, nil
}

// writeRecord writes the folder, fills paths with each attachment's file, and
// leaves files 0444 and directories 0555.
func (k *Kernel) writeRecord(it item.Item, atts []item.Attachment, bodies [][]byte, children []item.Item, paths []string) (string, error) {
	parent := filepath.Join(k.Root, runsDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, it.ID+"-")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		_ = Record{Dir: dir}.Close()
		return "", err
	}
	adir := filepath.Join(dir, "attachments")
	if len(atts) > 0 {
		if err := os.Mkdir(adir, 0o755); err != nil {
			return fail(err)
		}
	}
	used := map[string]bool{}
	for i, a := range atts {
		base := fileName(a.Name)
		f := base
		for n := 2; used[f]; n++ {
			f = fmt.Sprintf("%s-%d", base, n)
		}
		used[f] = true
		paths[i] = filepath.Join(adir, f)
		if err := os.WriteFile(paths[i], bodies[i], 0o444); err != nil {
			return fail(err)
		}
	}
	var cb strings.Builder
	for _, c := range children {
		fmt.Fprintf(&cb, "- %s [%s] %s\n", c.ID, c.Status, c.Title)
	}
	if err := os.WriteFile(filepath.Join(dir, "children.md"), []byte(cb.String()), 0o444); err != nil {
		return fail(err)
	}
	for _, d := range []string{adir, dir} {
		if _, err := os.Stat(d); err == nil {
			if err := os.Chmod(d, 0o555); err != nil {
				return fail(err)
			}
		}
	}
	return dir, nil
}

// clip cuts s to at most max bytes at a rune boundary.
func clip(s string, max int) (string, int) {
	if len(s) <= max {
		return s, len(s)
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], n
}

// renderRecord is the prompt section: with a folder, its paths; then the
// inline bodies, newest first, within the bounds; then the children.
func renderRecord(atts []item.Attachment, bodies [][]byte, paths []string, children []item.Item, dir, childrenPath string) string {
	var b strings.Builder
	where := func(i int) string {
		if dir == "" {
			return ""
		}
		return "; full body: " + paths[i]
	}
	b.WriteString("\n")
	if dir != "" {
		fmt.Fprintf(&b, "%s%s:\n", RecordLead, dir)
		for i, a := range atts {
			fmt.Fprintf(&b, "- %s (%s, %s, %d bytes, sha256 %s)\n", paths[i], a.Name, a.MediaType, a.Size, ledger.ShortHash(a.SHA256))
		}
		fmt.Fprintf(&b, "- %s (%d children)\n", childrenPath, len(children))
		b.WriteString("Read a file there for its full body; a body below may be cut.\n")
	} else {
		b.WriteString("The item's record (attachments and children), inline:\n")
	}
	spent := 0
	for i, a := range atts {
		body := bodies[i]
		fmt.Fprintf(&b, "\nAttachment %s (%s, %d bytes", a.Name, a.MediaType, len(body))
		if i == 0 {
			b.WriteString(", newest")
		}
		b.WriteString("):\n")
		if !utf8.Valid(body) {
			fmt.Fprintf(&b, "[shown 0 of %d bytes: not UTF-8 text%s]\n", len(body), where(i))
			continue
		}
		room := InlineTotalMax - spent
		limit := min(InlineBodyMax, room)
		shown, n := clip(string(body), limit)
		spent += n
		b.WriteString(shown)
		switch {
		case n == len(body):
			if !strings.HasSuffix(shown, "\n") {
				b.WriteString("\n")
			}
		case limit < InlineBodyMax:
			if n > 0 && !strings.HasSuffix(shown, "\n") {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "[shown %d of %d bytes: the %d-byte inline budget is spent%s]\n", n, len(body), InlineTotalMax, where(i))
		default:
			if !strings.HasSuffix(shown, "\n") {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "[shown %d of %d bytes%s]\n", n, len(body), where(i))
		}
	}
	if len(children) > 0 {
		b.WriteString("\nChildren:\n")
		for i, c := range children {
			if i == InlineChildrenMax {
				if dir != "" {
					fmt.Fprintf(&b, "[… %d more children; all are listed in %s]\n", len(children)-i, childrenPath)
				} else {
					fmt.Fprintf(&b, "[… %d more children not shown]\n", len(children)-i)
				}
				break
			}
			fmt.Fprintf(&b, "- %s [%s] %s\n", c.ID, c.Status, c.Title)
		}
	}
	return b.String()
}
