package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func init() {
	Register(&Verb{Name: "story-doc-attach", Description: "Attach a typed markdown document to a story", Invoke: storyDocAttach})
	Register(&Verb{Name: "story-doc-list", Description: "List a story's attached documents", Invoke: storyDocList})
	Register(&Verb{Name: "story-doc-get", Description: "Read one of a story's attached documents", Invoke: storyDocGet})
	Register(&Verb{Name: "story-lessons-list", Description: "List typed lessons attachments across all stories", Invoke: storyLessonsList})
}

// KindStoryDocAttached records a document attachment on the story's ledger.
const KindStoryDocAttached = ledger.KindStoryDocAttached

// attachmentDir resolves an item's attachment/bundle directory by KIND
// (sty_890b86cb). A story's docs live under the home-keyed runtime stories dir
// (wired via SetStoryDir → ~/.satelle/<repo-key>/stories/<id>/, sty_4660bbe1);
// a TASK or EXECUTION is NOT a story: its artifacts belong in the task's own
// bundle under .satelle/tasks/<tsk_id>/ (an execution roots at its PARENT task's
// folder). This closes the kind-blind root where `attach` on a task materialised
// under a story-shaped path. Empty when the dir wiring for that kind is unset.
func attachmentDir(it workitem.Item) string {
	switch it.Kind {
	case workitem.KindTask:
		if taskDir == "" {
			return ""
		}
		return filepath.Join(taskDir, it.ID)
	case workitem.KindExecution:
		if taskDir == "" || it.ParentID == "" {
			return ""
		}
		return filepath.Join(taskDir, it.ParentID)
	default:
		if storyDir == "" {
			return ""
		}
		return filepath.Join(storyDir, it.ID)
	}
}

// resolveAttachmentDir looks up an item by id and returns its kind-resolved
// attachment dir, or an error if the item is unknown / its dir wiring is unset.
func resolveAttachmentDir(ctx context.Context, store *workitem.Store, id string) (string, error) {
	it, err := store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	dir := attachmentDir(it)
	if dir == "" {
		return "", fmt.Errorf("verb: attachment dir for %s (%s) not configured", id, it.Kind)
	}
	return dir, nil
}

// baseSafe reduces a doc name to a bare filename with no path traversal.
// Shared by safeName (markdown) and safeBinaryName so the traversal rule has
// one definition (sty_40e5a305).
func baseSafe(name string) string {
	n := filepath.Base(strings.TrimSpace(name))
	if n == "" || n == "." || n == ".." || n == string(filepath.Separator) {
		return ""
	}
	return n
}

// safeName reduces a doc name to a bare filename (no path traversal) and ensures
// a single .md extension. Markdown path only — binary names use safeBinaryName.
func safeName(name string) string {
	n := baseSafe(name)
	if n == "" {
		return ""
	}
	return strings.TrimSuffix(n, ".md") + ".md"
}

type docAttachReq struct {
	StoryID string `json:"story_id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Body    string `json:"body"`
}

type docRef struct {
	StoryID     string `json:"story_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Body        string `json:"body,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Binary      bool   `json:"binary,omitempty"`
}

func storyDocAttach(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	var req docAttachReq
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	item, err := store.Get(ctx, req.StoryID)
	if err != nil {
		return nil, fmt.Errorf("verb: attach: %s: %w", req.StoryID, err)
	}
	name, typ, err := writeAttachedDoc(ctx, item, req.Name, req.Type, req.Body, time.Now())
	if err != nil {
		return nil, err
	}
	return json.Marshal(docRef{StoryID: req.StoryID, Name: name, Type: typ})
}

// writeAttachedDoc materialises a typed markdown document into an item's
// kind-resolved attachment dir (runtime stories/<id>/ or .satelle/tasks/<id>/)
// with the standard frontmatter, and records a KindStoryDocAttached ledger row. It
// is the shared core of the story-doc-attach verb and the step-summary deposit
// (sty_47d31300), so a dispatched agent can PULL prior step summaries via
// `story docs`/`story doc` — the same path it pulls the plan. Returns the bare
// (extension-less) doc name and the normalised type.
func writeAttachedDoc(ctx context.Context, item workitem.Item, name, typ, body string, now time.Time) (string, string, error) {
	file := safeName(name)
	if file == "" {
		return "", "", fmt.Errorf("verb: attach: a document name is required")
	}
	typ = strings.TrimSpace(typ)
	if typ == "" {
		typ = "document"
	}
	dir := attachmentDir(item)
	if dir == "" {
		return "", "", fmt.Errorf("verb: attach: attachment dir for %s (%s) not configured", item.ID, item.Kind)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("verb: attach: %w", err)
	}
	bare := strings.TrimSuffix(file, ".md")
	var b strings.Builder
	fmt.Fprintf(&b, "---\nstory: %s\ntype: %s\nname: %s\n---\n\n%s\n",
		item.ID, typ, bare, strings.TrimRight(body, "\n"))
	if err := os.WriteFile(filepath.Join(dir, file), []byte(b.String()), 0o644); err != nil {
		return "", "", fmt.Errorf("verb: attach: %w", err)
	}
	payload, _ := json.Marshal(map[string]string{"name": bare, "type": typ})
	appendLedgerEntry(ctx, item.ID, KindStoryDocAttached, "",
		fmt.Sprintf("attached %s document %q", typ, bare), payload, now)
	return bare, typ, nil
}

// AttachItemDoc exposes the verb-owned typed attachment mechanism to the
// isolated-agent engine. The engine validates structured output first; this
// function writes it through the same path as `satelle story attach`.
func AttachItemDoc(ctx context.Context, item workitem.Item, name, typ, body string) (string, string, error) {
	return writeAttachedDoc(ctx, item, name, typ, body, time.Now())
}

// HasStoryDoc reports whether the story already holds an attachment of that
// name. It reads the directory entry only, never the body.
func HasStoryDoc(ctx context.Context, storyID, name string) bool {
	store, err := requireWorkItem()
	if err != nil {
		return false
	}
	dir, err := resolveAttachmentDir(ctx, store, storyID)
	if err != nil {
		return false
	}
	file := safeName(name)
	if file == "" {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, file))
	return err == nil
}

// storyLessonsList walks every story attachment dir and returns docs whose
// frontmatter type is lesson or lessons — the offline friction corpus
// (epic:substrate-convergence order:9). Repo-agnostic listing mechanism.
func storyLessonsList(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if storyDir == "" {
		return nil, fmt.Errorf("verb: story dir not configured")
	}
	entries, err := os.ReadDir(storyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return json.Marshal([]docRef{})
		}
		return nil, fmt.Errorf("verb: lessons list: %w", err)
	}
	out := []docRef{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		storyID := e.Name()
		if !strings.HasPrefix(storyID, "sty_") {
			continue
		}
		sub := filepath.Join(storyDir, storyID)
		files, _ := os.ReadDir(sub)
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(sub, f.Name()))
			if rerr != nil {
				continue
			}
			typ, name := docMeta(string(data))
			lt := strings.ToLower(strings.TrimSpace(typ))
			if lt != "lesson" && lt != "lessons" {
				continue
			}
			if name == "" {
				name = strings.TrimSuffix(f.Name(), ".md")
			}
			out = append(out, docRef{StoryID: storyID, Name: name, Type: typ})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StoryID != out[j].StoryID {
			return out[i].StoryID < out[j].StoryID
		}
		return out[i].Name < out[j].Name
	})
	return json.Marshal(out)
}

func storyDocList(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	var req docRef
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	dir, err := resolveAttachmentDir(ctx, store, req.StoryID)
	if err != nil {
		return nil, err
	}
	out := []docRef{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		typ, name := docMeta(string(data))
		if name == "" {
			name = strings.TrimSuffix(e.Name(), ".md")
		}
		out = append(out, docRef{StoryID: req.StoryID, Name: name, Type: typ})
	}
	// Binary attachments via sidecar (sty_40e5a305); markdown path unchanged.
	out = append(out, listBinaryDocs(req.StoryID, dir)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return json.Marshal(out)
}

func storyDocGet(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	var req docRef
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	dir, err := resolveAttachmentDir(ctx, store, req.StoryID)
	if err != nil {
		return nil, err
	}
	// Binary attachments must not stream raw bytes through the markdown get path
	// (sty_40e5a305 AC6). Surface an actionable error pointing at --out.
	if sc, ok := isBinaryAttachment(dir, req.Name); ok {
		return nil, fmt.Errorf("verb: doc: %s is a binary attachment (%s, %d bytes); read it with: satelle story doc %s %s --out <path>",
			sc.Name, sc.ContentType, sc.Size, req.StoryID, sc.Name)
	}
	file := safeName(req.Name)
	if file == "" {
		return nil, fmt.Errorf("verb: doc: a document name is required")
	}
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return nil, fmt.Errorf("verb: doc %s/%s: %w", req.StoryID, req.Name, err)
	}
	typ, name := docMeta(string(data))
	if name == "" {
		name = strings.TrimSuffix(file, ".md")
	}
	return json.Marshal(docRef{StoryID: req.StoryID, Name: name, Type: typ, Body: string(data)})
}

// DocBody is one attachment's name/type/body for payload injection (sty_58fa970e).
// Exported so agentstep can resolve docs without reaching into unexported storyDir.
// Binary attachments set Binary=true with an empty Body (sty_40e5a305) so gates
// learn a binary exists without consuming docsPayloadCeiling.
type DocBody struct {
	Name string
	Type string
	Body string
	// Path is the file the body was read from, so a dispatch log can name the
	// document instead of copying it (sty_2b89feab). Empty for a binary entry.
	Path        string
	Binary      bool
	ContentType string
	Size        int64
	SHA256      string
}

// ItemDocs returns every attachment for the item, name-sorted, with full bodies.
// Used by the isolated-agent payload docs injector so Bash-less reviewers can
// judge without any disk path. Empty slice (not error) when the dir is missing.
func ItemDocs(ctx context.Context, id string) ([]DocBody, error) {
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	dir, err := resolveAttachmentDir(ctx, store, id)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("verb: item docs: %w", err)
	}
	var out []DocBody
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		typ, name := docMeta(string(data))
		if name == "" {
			name = strings.TrimSuffix(e.Name(), ".md")
		}
		out = append(out, DocBody{Name: name, Type: typ, Body: string(data), Path: path})
	}
	// Reference-only binary entries (no body) — fillPayloadDocs skips them
	// before the budget accumulator (sty_40e5a305 AC4).
	out = append(out, binaryDocBodies(dir)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// AttachmentFile is one markdown attachment as it sits on disk: its file name
// (with the .md extension) and its raw bytes, frontmatter included.
type AttachmentFile struct {
	Name    string
	Content []byte
}

// safeItemID reports whether id is a bare directory name, so an id taken from the
// wire or from disk can never address a path outside the attachment root.
func safeItemID(id string) bool {
	return id != "" && baseSafe(id) == id
}

// StoryAttachmentIDs lists the ids of the stories that have an attachment dir
// under the runtime stories dir. Empty (not an error) when the dir is missing, so
// a machine that never attached anything has nothing to mirror.
func StoryAttachmentIDs() ([]string, error) {
	if storyDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(storyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("verb: story attachment ids: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "sty_") {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// StoryAttachmentFiles returns a story's markdown attachment files, name-sorted,
// with their raw bytes, and the number of binary attachments (those carrying a
// sidecar), which are never returned: the hosted documents store holds text only.
// Empty (not an error) when the story has no attachment dir.
func StoryAttachmentFiles(id string) (files []AttachmentFile, binary int, err error) {
	if storyDir == "" || !safeItemID(id) {
		return nil, 0, nil
	}
	dir := filepath.Join(storyDir, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("verb: story attachment files: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if file, ok := strings.CutSuffix(name, SidecarSuffix); ok {
			if _, serr := os.Stat(filepath.Join(dir, file)); serr == nil {
				binary++
			}
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		if _, serr := os.Stat(filepath.Join(dir, name+SidecarSuffix)); serr == nil {
			continue // a binary attachment that happens to end in .md
		}
		data, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			return nil, 0, fmt.Errorf("verb: story attachment files: %w", rerr)
		}
		files = append(files, AttachmentFile{Name: name, Content: data})
	}
	return files, binary, nil
}

// WriteStoryAttachmentFile stores one markdown attachment file under the story's
// attachment dir exactly as given: the frontmatter is already inside content, so
// a file mirrored from another machine reads back byte for byte. Unlike
// writeAttachedDoc it records no ledger row — the attachment was recorded on the
// machine that made it, and that row travels with the ledger area.
func WriteStoryAttachmentFile(id, file string, content []byte) error {
	if storyDir == "" {
		return fmt.Errorf("verb: story dir not configured")
	}
	if !safeItemID(id) {
		return fmt.Errorf("verb: attachment story id %q is not a bare id", id)
	}
	if file == "" || baseSafe(file) != file || !strings.HasSuffix(file, ".md") {
		return fmt.Errorf("verb: attachment file %q is not a bare markdown file name", file)
	}
	dir := filepath.Join(storyDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("verb: write attachment: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), content, 0o644); err != nil {
		return fmt.Errorf("verb: write attachment: %w", err)
	}
	return nil
}

// docMeta pulls the type/name from a doc file's frontmatter (best-effort). The
// returned values feed the doc list/get views.
func docMeta(s string) (typ, name string) {
	if !strings.HasPrefix(s, "---\n") {
		return "", ""
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "type":
			typ = strings.TrimSpace(v)
		case "name":
			name = strings.TrimSpace(v)
		}
	}
	return typ, name
}
