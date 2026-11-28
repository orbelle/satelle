package kernel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/procgroup"
)

const (
	InlineBodyMax     = 16 << 10
	InlineTotalMax    = 64 << 10
	InlineChildrenMax = 200
	RejectNotesMax    = 8 << 10
	RecordLead        = "The item's record is in the read-only folder "
	runsDir           = "runs"
	ownerExt          = ".owner"
	claimTries        = 100
)

type recordView int

const (
	recordInline recordView = iota
	recordFolder
	recordPointers
)

type Record struct {
	Dir     string
	Section string
	Scratch string
	tmp     string
}

func (r Record) Close() error {
	var err error
	if r.Dir != "" {
		err = removeRun(r.Dir)
	}
	if r.tmp != "" {
		if terr := removeRun(r.tmp); err == nil {
			err = terr
		}
	}
	return err
}

func removeRun(dir string) error {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Remove(dir + ownerExt); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

type ownerState int

const (
	ownerMissing ownerState = iota
	ownerBad
	ownerGone
	ownerKept
)

func runOwner(dir string) ownerState {
	b, err := os.ReadFile(dir + ownerExt)
	if errors.Is(err, fs.ErrNotExist) {
		return ownerMissing
	}
	var p procgroup.Proc
	if err != nil || json.Unmarshal(b, &p) != nil {
		return ownerBad
	}
	if procgroup.Probe(p) == procgroup.Gone {
		return ownerGone
	}
	return ownerKept
}

func sweepRuns(parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	dirs := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			dirs[e.Name()] = true
		}
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir():
			if s := runOwner(filepath.Join(parent, name)); s == ownerMissing || s == ownerGone {
				_ = removeRun(filepath.Join(parent, name))
			}
		case strings.HasSuffix(name, ownerExt) && e.Type().IsRegular():
			dir := strings.TrimSuffix(name, ownerExt)
			if dir != "" && !dirs[dir] && runOwner(filepath.Join(parent, dir)) == ownerGone {
				_ = os.Remove(filepath.Join(parent, name))
			}
		}
	}
}

func claimRun(parent, id string) (string, error) {
	owner, err := json.Marshal(procgroup.Self())
	if err != nil {
		return "", err
	}
	for range claimTries {
		dir := filepath.Join(parent, id+"-"+strconv.FormatUint(uint64(rand.Uint32()), 10))
		f, err := os.OpenFile(dir+ownerExt, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(owner)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(dir + ownerExt)
			return "", werr
		}
		if err := os.Mkdir(dir, 0o755); err != nil {
			_ = os.Remove(dir + ownerExt)
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return "", err
		}
		return dir, nil
	}
	return "", fmt.Errorf("%s: no free run folder name under %s after %d tries", id, parent, claimTries)
}

var hexSHA = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

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

func (k *Kernel) OpenRecord(id string) (Record, error) {
	it, events, err := k.load(id)
	if err != nil {
		return Record{}, err
	}
	return k.record(it, events, recordFolder)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func fileName(name string) string {
	f := unsafeName.ReplaceAllString(name, "_")
	if f == "" || f == "." || f == ".." {
		return "_"
	}
	return f
}

func (k *Kernel) record(it item.Item, events []ledger.Event, view recordView) (Record, error) {
	parent := func() (string, error) {
		p, err := k.recordsParent()
		if err == nil {
			sweepRuns(p)
		}
		return p, err
	}
	return k.recordUnder(it, events, view, parent)
}

func (k *Kernel) targetRecord(it item.Item, events []ledger.Event, view recordView, wt worktree) (Record, error) {
	tmp, err := os.MkdirTemp("", "satelle-target-")
	if err != nil {
		return Record{}, err
	}
	fail := func(err error) (Record, error) {
		_ = removeRun(tmp)
		return Record{}, err
	}
	real, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return fail(err)
	}
	for _, tree := range []string{k.Dir, wt.dir} {
		if tree == "" {
			continue
		}
		rt, err := filepath.EvalSymlinks(tree)
		if err != nil {
			return fail(err)
		}
		if rel, err := filepath.Rel(rt, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fail(fmt.Errorf("target record folder %s lies inside %s; set TMPDIR outside the control and target checkouts", real, rt))
		}
	}
	scratch := filepath.Join(real, "cwd")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		return fail(err)
	}
	rec, err := k.recordUnder(it, events, view, func() (string, error) { return real, nil })
	if err != nil {
		return fail(err)
	}
	rec.Scratch, rec.tmp = scratch, real
	return rec, nil
}

func (k *Kernel) recordUnder(it item.Item, events []ledger.Event, view recordView, parent func() (string, error)) (Record, error) {
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
	if view != recordInline {
		p, err := parent()
		if err != nil {
			return Record{}, err
		}
		if rec.Dir, err = writeRecord(p, it, atts, bodies, children, paths); err != nil {
			return Record{}, err
		}
		childrenPath = filepath.Join(rec.Dir, "children.md")
	}
	rec.Section = renderRecord(atts, bodies, paths, children, rec.Dir, childrenPath, view == recordPointers)
	return rec, nil
}

func writeRecord(parent string, it item.Item, atts []item.Attachment, bodies [][]byte, children []item.Item, paths []string) (string, error) {
	dir, err := claimRun(parent, it.ID)
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

func (k *Kernel) recordsParent() (string, error) {
	if k.RecordsDir == "" {
		parent := filepath.Join(k.Root, runsDir)
		return parent, os.MkdirAll(parent, 0o755)
	}
	check := []string{k.RecordsDir}
	if rel, err := filepath.Rel(k.Dir, k.RecordsDir); err == nil && k.Dir != "" && rel != "." && !strings.HasPrefix(rel, "..") {
		check = append(check, k.Dir)
		p := k.Dir
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			p = filepath.Join(p, part)
			check = append(check, p)
		}
	}
	for _, p := range check {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() {
			return "", fmt.Errorf("record folders: %s is not a directory", p)
		}
	}
	return k.RecordsDir, nil
}

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

func renderRecord(atts []item.Attachment, bodies [][]byte, paths []string, children []item.Item, dir, childrenPath string, pointersOnly bool) string {
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
		if pointersOnly {
			b.WriteString("Read a file there for its full body; no attachment body is in this prompt.\n")
		} else {
			b.WriteString("Read a file there for its full body; a body below may be cut.\n")
		}
	} else {
		b.WriteString("The item's record (attachments and children), inline:\n")
	}
	inline := atts
	if pointersOnly {
		inline = nil
	}
	spent := 0
	for i, a := range inline {
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
