package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func TestSplitCriteria(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   \n ", nil},
		{"just one criterion", []string{"just one criterion"}},
		{"1. first\n2. second\n   continued\n3) third", []string{"first", "second\n   continued", "third"}},
		{"preamble\n- a\n* b", []string{"preamble", "a", "b"}},
	} {
		if got := SplitCriteria(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitCriteria(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMapPrecedence(t *testing.T) {
	m := &StatusMap{Status: map[string]Target{
		"active":      {Workflow: "w", State: "a"},
		"task/active": {Workflow: "standing", State: "open"},
	}}
	if got, key, ok := m.Resolve("task", "active"); !ok || got.Workflow != "standing" || key != "task/active" {
		t.Errorf("task active -> %v %s %v", got, key, ok)
	}
	if got, key, ok := m.Resolve("story", "active"); !ok || got.Workflow != "w" || key != "active" {
		t.Errorf("story active -> %v %s %v", got, key, ok)
	}
	if _, _, ok := m.Resolve("story", "gone"); ok {
		t.Error("an unmapped status resolved")
	}
	got := m.unmapped([]OldItem{{Kind: "story", Status: "gone"}, {Kind: "story", Status: "gone"}, {Kind: "task", Status: "active"}})
	if !reflect.DeepEqual(got, []string{"story gone: 2 item(s)"}) {
		t.Errorf("unmapped = %q", got)
	}
}

// NewEventWith hashes exactly as NewEvent does for the same inputs.
func TestNewEventWithMatchesNewEvent(t *testing.T) {
	s, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	s.Now = func() time.Time { return at }
	e, err := s.NewEvent("sto_1", "k", "me", map[string]string{"a": "b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := ledger.NewEventWith(e.ID, at, "sto_1", "k", "me", map[string]string{"a": "b"}, "")
	if err != nil || f.Hash != e.Hash {
		t.Fatalf("hashes differ: %s vs %s (%v)", f.Hash, e.Hash, err)
	}
}

// oldTree writes a small pre-1.0 .satelle with a 0755 file and a symlink.
func oldTree(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), ".satelle")
	for p, body := range map[string]string{"satelle.toml": "[review]\n", "deployed.version": "0.0.1\n", "workflows/step.toml": "[meta]\n", "skills/x.md": "x"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(d, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, "tool.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere/logs", filepath.Join(d, "logs")); err != nil {
		t.Fatal(err)
	}
	return d
}

// Across devices the tree is copied, verified, and only then removed; a
// copy that does not match leaves the source in place.
func TestArchiveCrossDevice(t *testing.T) {
	defer func() { rename, copyHook = os.Rename, func(string) {} }()
	rename = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EXDEV} }
	src := oldTree(t)
	before, _ := Manifest(src)
	a, err := Archive(src, t.TempDir(), time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still there: %v", err)
	}
	after, _ := Manifest(a.Dest)
	if err := sameManifest(before, after); err != nil {
		t.Fatal(err)
	}

	src = oldTree(t)
	copyHook = func(dst string) { _ = os.WriteFile(filepath.Join(dst, "skills/x.md"), []byte("damaged"), 0o644) }
	if _, err := Archive(src, t.TempDir(), time.Now(), false); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("damaged copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "skills/x.md")); err != nil {
		t.Errorf("the source was removed after a bad copy: %v", err)
	}
}

func TestArchiveRefusesV1AndExistingDest(t *testing.T) {
	d := filepath.Join(t.TempDir(), ".satelle")
	_ = os.MkdirAll(d, 0o755)
	if _, err := Archive(d, t.TempDir(), time.Now(), false); err == nil || !strings.Contains(err.Error(), "not a pre-1.0") {
		t.Errorf("v1 tree: %v", err)
	}
	src, rt, now := oldTree(t), t.TempDir(), time.Now()
	dest := filepath.Join(rt, "archive", now.UTC().Format("20060102T150405Z"), ".satelle")
	_ = os.MkdirAll(dest, 0o755)
	if _, err := Archive(src, rt, now, false); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("existing destination: %v", err)
	}
}

func TestRefusalIsRecognised(t *testing.T) {
	err := error(&Refusal{Reasons: []string{"x"}})
	if !IsRefusal(err) || IsRefusal(errors.New("y")) {
		t.Error("IsRefusal")
	}
}

// The new files name none of the start-clean repos: the list is machine
// configuration only (satelle-server is also the product's name elsewhere,
// so the scan covers this change's files).
func TestNoRepoNameInTheUpgradeCode(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	files = append(files, "../cli/cmd_upgrade.go", "../kernel/legacy.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"satelle-server", "satelle-delivery", "satelle-server-infra"} {
			if strings.Contains(string(b), name) {
				t.Errorf("%s names %s", f, name)
			}
		}
	}
}
