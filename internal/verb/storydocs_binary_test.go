package verb_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// minimalPNG is a tiny valid 1×1 PNG (includes a \x00 and the sequence that
// would look like frontmatter if text-handled).
func minimalPNG() []byte {
	// 1x1 transparent PNG
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR len+type
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
		0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, // IDAT
		0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
		0x0d, 0x0a, 0x2d, 0xb4,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, // IEND
		0xae, 0x42, 0x60, 0x82,
		// plant frontmatter-looking bytes that must survive byte-exact
		0x0a, 0x2d, 0x2d, 0x2d, 0x0a, 0x00,
	}
}

func minimalPDF() []byte {
	return []byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n")
}

func wireDocs(t *testing.T) (storyDir string) {
	t.Helper()
	withWiring(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "satelle.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	storyDir = filepath.Join(dir, "stories")
	verb.SetWorkItemStore(db.Stories)
	verb.SetLedgerStore(db.Ledger)
	verb.SetTxRunner(db.InTx)
	verb.SetDocIndexStore(db.DocIndex)
	verb.SetStoryDir(storyDir)
	verb.SetAttachmentPolicy(0, nil) // defaults
	t.Cleanup(func() {
		db.Close()
	})
	return storyDir
}

func createStory(t *testing.T) workitem.Item {
	t.Helper()
	var st workitem.Item
	if err := json.Unmarshal(call(t, "story-create", map[string]any{
		"title": "Binary docs", "acceptance_criteria": "1. ok",
	}), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestMarkdownAttachGolden (AC2): byte-for-byte identical markdown attach
// output including frontmatter — tripwire for baseSafe extraction.
func TestMarkdownAttachGolden(t *testing.T) {
	storyDir := wireDocs(t)
	st := createStory(t)
	call(t, "story-doc-attach", map[string]any{
		"story_id": st.ID, "name": "note", "type": "note",
		"body": "hello body",
	})
	got, err := os.ReadFile(filepath.Join(storyDir, st.ID, "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nstory: " + st.ID + "\ntype: note\nname: note\n---\n\nhello body\n"
	if string(got) != want {
		t.Fatalf("markdown path mutated:\n got %q\nwant %q", got, want)
	}
}
