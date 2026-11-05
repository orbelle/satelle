package doctor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/testutil"
)

// unreadableWorkflows lays out a data dir whose workflows path is a regular
// file — unreadable for root too, unlike a chmod fixture.
func unreadableWorkflows(t *testing.T) (dataDir, wfPath string) {
	t.Helper()
	dataDir = t.TempDir()
	wfPath = filepath.Join(dataDir, "workflows")
	if err := os.WriteFile(wfPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dataDir, wfPath
}

// sty_d6e209aa AC1: the store-backed loader and doctor's store-free loader cannot
// disagree about the same dir — both answer with the one sentinel, from the one
// predicate.
func TestBothLoadersAgreeOnAnUnreadableWorkflowsDir(t *testing.T) {
	dataDir, wfPath := unreadableWorkflows(t)

	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "idx.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := docindex.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store := docindex.New(db)
	store.SetRoots(map[string]string{"workflows": wfPath})
	fromStore, err := store.List(context.Background(), "workflows")
	if err != nil {
		t.Fatal(err)
	}
	fromDisk := WorkflowDocs(dataDir)

	sp, sr, sok := docindex.UnreadableOf(fromStore)
	dp, dr, dok := docindex.UnreadableOf(fromDisk)
	if !sok || !dok || sp != dp || sr != dr || sp != wfPath {
		t.Fatalf("loaders disagree: store (%q, %q, %v) vs disk (%q, %q, %v)", sp, sr, sok, dp, dr, dok)
	}
	if len(fromStore) != 1 || len(fromDisk) != 1 {
		t.Fatalf("each loader must return only the sentinel, got %d and %d docs", len(fromStore), len(fromDisk))
	}
}

// Doctor reports the unreadable dir as an ERROR, once, and judges no embedded
// route as if it governed.
func TestCheckReportsAnUnreadableWorkflowsDirOnceAsAnError(t *testing.T) {
	testutil.IsolateHome(t)
	dataDir, _ := unreadableWorkflows(t)
	rep := Check(context.Background(), Opts{RepoRoot: filepath.Dir(dataDir), DataDir: dataDir})
	var hits health.Findings
	for _, f := range rep.Findings {
		if f.ID == health.IDProcessUnreadable {
			hits = append(hits, f)
		}
		if f.ID == health.IDNodeAlloc || f.ID == health.IDHookAlloc {
			t.Errorf("an allocation finding about the embedded route was reported as if it governed: %v", f)
		}
	}
	if len(hits) != 1 || hits[0].Severity != health.SeverityError {
		t.Fatalf("want exactly one %s error finding, got %v", health.IDProcessUnreadable, hits)
	}
	for _, want := range []string{"embedded default route", "gates would not be the repository's gates"} {
		if !strings.Contains(hits[0].Detail, want) {
			t.Errorf("finding must carry %q: %s", want, hits[0].Detail)
		}
	}
	if rep.OK {
		t.Error("an unreadable authored process is not healthy")
	}
}

// An absent dir is a WARNING naming the backstop, never an error.
func TestCheckWarnsOnAnAbsentWorkflowsDir(t *testing.T) {
	testutil.IsolateHome(t)
	dataDir := t.TempDir()
	rep := Check(context.Background(), Opts{RepoRoot: filepath.Dir(dataDir), DataDir: dataDir})
	var hits health.Findings
	for _, f := range rep.Findings {
		if f.ID == health.IDProcessAbsent {
			hits = append(hits, f)
		}
		if f.ID == health.IDProcessUnreadable {
			t.Errorf("an absent dir must not be reported as unreadable: %v", f)
		}
	}
	if len(hits) != 1 || hits[0].Severity != health.SeverityWarn {
		t.Fatalf("want exactly one %s warning, got %v", health.IDProcessAbsent, hits)
	}
	if !strings.Contains(hits[0].Detail, "binary's defaults, not authored ones") {
		t.Errorf("warning must say the gates are the binary's: %s", hits[0].Detail)
	}
}

// AC6: the path doctor names comes from the resolved process config, not from a
// <data dir>/workflows assumption — a [substrate_roots] override moves it.
func TestCheckAbsentWarningNamesTheResolvedWorkflowsDir(t *testing.T) {
	testutil.IsolateHome(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "cfg", "satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	process := config.Config{
		DataDir:        "cfg/satelle",
		SubstrateRoots: map[string]string{"workflows": "elsewhere"},
	}
	want := config.ResolveAuthoredRoots(process, root, "").Dirs["workflows"]
	if want == filepath.Join(dataDir, "workflows") {
		t.Fatalf("fixture must relocate workflows, got %s", want)
	}
	rep := Check(context.Background(), Opts{
		RepoRoot: root, DataDir: dataDir, Process: &process, ProcessRoot: root,
	})
	for _, f := range rep.Findings {
		if f.ID == health.IDProcessAbsent {
			if !strings.Contains(f.Detail, want) {
				t.Errorf("absent warning must name the resolved path %q: %s", want, f.Detail)
			}
			return
		}
	}
	t.Fatalf("no %s finding: %v", health.IDProcessAbsent, rep.Findings)
}

// AC1+AC6: an UNREADABLE workflows dir that a [substrate_roots] override moved off
// <data dir>/workflows is still reported as the process-unreadable error — once,
// naming the resolved path — and no embedded route is judged as if it governed.
func TestCheckReportsAnUnreadableRelocatedWorkflowsDir(t *testing.T) {
	testutil.IsolateHome(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "cfg", "satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	process := config.Config{
		DataDir:        "cfg/satelle",
		SubstrateRoots: map[string]string{"workflows": "elsewhere"},
	}
	wfPath := config.ResolveAuthoredRoots(process, root, "").Dirs["workflows"]
	if wfPath == filepath.Join(dataDir, "workflows") {
		t.Fatalf("fixture must relocate workflows, got %s", wfPath)
	}
	if err := os.MkdirAll(filepath.Dir(wfPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wfPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := Check(context.Background(), Opts{
		RepoRoot: root, DataDir: dataDir, Process: &process, ProcessRoot: root,
	})
	var hits health.Findings
	for _, f := range rep.Findings {
		switch f.ID {
		case health.IDProcessUnreadable:
			hits = append(hits, f)
		case health.IDProcessAbsent:
			t.Errorf("an unreadable relocated dir must not be reported absent: %v", f)
		case health.IDNodeAlloc, health.IDHookAlloc:
			t.Errorf("an allocation finding about the embedded route was reported as if it governed: %v", f)
		}
	}
	if len(hits) != 1 || hits[0].Severity != health.SeverityError {
		t.Fatalf("want exactly one %s error finding, got %v", health.IDProcessUnreadable, hits)
	}
	for _, want := range []string{wfPath, "embedded default route", "gates would not be the repository's gates"} {
		if !strings.Contains(hits[0].Detail, want) {
			t.Errorf("finding must carry %q: %s", want, hits[0].Detail)
		}
	}
	if rep.OK {
		t.Error("an unreadable authored process is not healthy")
	}
}
