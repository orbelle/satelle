package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/snapsync"
)

// sty_1217a400: `sync pull` of an area whose hosted copy holds per-file heads and
// no snapshot (the plain-upload fallback) settles it into a snapshot and brings
// the files down, so `sync status` then reports it in step. The hosted copy is the
// fake of sync_snapshot_test.go, seeded the way cmd_sync_status_empty_area_test.go
// seeds a hosted-only area.

// recordFiles is the files the area's snapshot record at version names, or nil
// when the fake holds no such record.
func recordFiles(t *testing.T, f *snapFake, area string, version int) map[string]string {
	t.Helper()
	body, _, ok := f.cfg.get("probe", snapsync.RecordPath(area), version)
	if !ok {
		return nil
	}
	rec, err := snapsync.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Files
}

// recordPuts counts the snapshot-record PUTs the fake received.
func recordPuts(f *snapFake) int {
	n := 0
	for _, p := range f.putLog() {
		if strings.HasPrefix(p, "config:") {
			if _, ok := snapsync.AreaOfRecordPath(strings.TrimPrefix(p, "config:")); ok {
				n++
			}
		}
	}
	return n
}

func TestSyncPullSettlesHeadsOnlyArea(t *testing.T) {
	const team = "team skill\n"
	for _, flag := range []string{"", "--fast-forward-only"} {
		t.Run("flag="+flag, func(t *testing.T) {
			m, f := scaffoldOnlyMachine(t)
			f.cfg.put("probe", "skills/team.md", []byte(team))
			before, _ := m.status()
			wantState(t, before, "skills", "behind")
			putsBefore := len(f.putLog())

			var args []string
			if flag != "" {
				args = append(args, flag)
			}
			out, err := m.pull(args...)
			if err != nil {
				t.Fatalf("pull: %v\n%s", err, out)
			}
			if got := m.read("skills/team.md"); got != team {
				t.Errorf("skills/team.md = %q, want the hosted bytes", got)
			}
			if got := recordFiles(t, f, "skills", 1); len(got) != 1 || got["skills/team.md"] != snapsync.SHA([]byte(team)) {
				t.Errorf("snapshot 1 = %v, want exactly the hosted head", got)
			}
			if puts := f.putLog()[putsBefore:]; len(puts) != 1 || puts[0] != "config:"+snapsync.RecordPath("skills") {
				t.Errorf("pull PUT %v, want the one snapshot record and no blob", puts)
			}
			// The fixture's settings area is never synced, so status exits non-zero;
			// the area under test is read from its own row.
			after, _ := m.status()
			if cols := wantState(t, after, "skills", "in step"); cols[4] != "-" {
				t.Errorf("skills next = %q, want -", cols[4])
			}
		})
	}
}

func TestSyncPullSettlesHeadsOnlyDocuments(t *testing.T) {
	const doc = "# team doc\n"
	m, f := scaffoldOnlyMachine(t)
	f.docs.put("probe", "documents/team.md", []byte(doc))
	before, _ := m.status()
	wantState(t, before, "documents", "behind")

	out, err := m.pull()
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if strings.Contains(out, "snapshot could not be read") {
		t.Errorf("a settled documents area must not report an unreadable snapshot:\n%s", out)
	}
	if got := m.read("documents/team.md"); got != doc {
		t.Errorf("documents/team.md = %q, want the hosted bytes", got)
	}
	if got := recordFiles(t, f, "documents", 1); len(got) != 1 || got["documents/team.md"] != snapsync.SHA([]byte(doc)) {
		t.Errorf("documents snapshot 1 = %v, want exactly the hosted head", got)
	}
	after, _ := m.status()
	wantState(t, after, "documents", "in step")
}

// A file that lands locally between the status and the pull is never overwritten
// and never claimed over: the settle publishes nothing and the area is turned away
// with a next that changes it.
func TestSyncPullHeadsOnlyNeverOverwritesLocal(t *testing.T) {
	for _, c := range []struct{ name, file, body string }{
		{"same path as a hosted head", "skills/team.md", "my own team skill\n"},
		{"another path", "skills/mine.md", "mine\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, f := scaffoldOnlyMachine(t)
			f.cfg.put("probe", "skills/team.md", []byte("team skill\n"))
			syncPullBeforeApply = func() { m.write(c.file, c.body) }
			t.Cleanup(func() { syncPullBeforeApply = nil })

			out, err := m.pull()
			if err == nil {
				t.Fatalf("a pull turned away must exit non-zero:\n%s", out)
			}
			if got := m.read(c.file); got != c.body {
				t.Errorf("%s = %q, want the local bytes kept", c.file, got)
			}
			if c.file != "skills/team.md" && m.has("skills/team.md") {
				t.Error("skills/team.md was fetched over a local change to the area")
			}
			if recordPuts(f) != 0 {
				t.Error("a snapshot was claimed over an area this machine holds files of")
			}
			if !strings.Contains(out, "skills: behind, left unchanged") || !strings.Contains(out, "next: satelle sync status") || strings.Contains(out, "next: satelle sync pull") {
				t.Errorf("the refusal must name the area and a next that is not pull:\n%s", out)
			}
		})
	}
}

// A hosted copy that takes a snapshot record but cannot read it back cannot hold a
// snapshot: the pull settles nothing, writes nothing, and names no pull.
func TestSyncPullHeadsOnlyUnreadableRecordsNamesNoPull(t *testing.T) {
	m, f := scaffoldOnlyMachine(t)
	f.cfg.put("probe", "skills/team.md", []byte("team skill\n"))
	f.unreadableRecords = true

	out, err := m.pull()
	if err == nil {
		t.Fatalf("a pull that could not settle the area must exit non-zero:\n%s", out)
	}
	if m.has("skills/team.md") {
		t.Error("a file was written although no snapshot could be settled")
	}
	if !strings.Contains(out, "cannot hold a snapshot") || strings.Contains(out, "next: satelle sync pull") {
		t.Errorf("the report must say why and name no pull:\n%s", out)
	}
}

// Another machine claiming the first snapshot between this one's prepare and its
// claim is benign: the pull fast-forwards onto the winner.
func TestSyncPullSettleRaceIsBenign(t *testing.T) {
	const team = "team skill\n"
	m, f := scaffoldOnlyMachine(t)
	f.cfg.put("probe", "skills/team.md", []byte(team))
	f.onRecordPut = func() {
		f.forgeRecord(t, "skills", 0, map[string]string{"skills/team.md": snapsync.SHA([]byte(team))})
	}

	out, err := m.pull()
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if got := m.read("skills/team.md"); got != team {
		t.Errorf("skills/team.md = %q, want the hosted bytes", got)
	}
	after, _ := m.status()
	wantState(t, after, "skills", "in step")
}

// voidBase records a base for the area from a store that no longer exists: its
// version is ahead of the hosted head.
func (m *snapMachine) voidBase(area string, files map[string]string) {
	m.t.Helper()
	b := hosted.AreaBase{Version: 3, Files: map[string]string{}}
	for p, body := range files {
		b.Files[p] = snapsync.SHA([]byte(body))
	}
	if err := hosted.SaveAreaBase(m.url, "probe", m.repo, area, b); err != nil {
		m.t.Fatal(err)
	}
}

// A replaced store leaves a base ahead of the hosted head. With local files it is
// never synced (push claims them); with none it is the heads-only area (pull
// settles it). Neither is behind-and-pull with nothing to pull.
func TestSyncVoidBaseIsNotADeadEnd(t *testing.T) {
	t.Run("local present", func(t *testing.T) {
		m, _ := scaffoldOnlyMachine(t)
		m.write("skills/mine.md", "mine\n")
		m.voidBase("skills", map[string]string{"skills/mine.md": "mine\n"})
		out, _ := m.status()
		if cols := wantState(t, out, "skills", "never synced"); cols[4] != "satelle sync push" {
			t.Errorf("skills next = %q, want satelle sync push", cols[4])
		}
		if out, err := m.pull(); err != nil || m.read("skills/mine.md") != "mine\n" {
			t.Errorf("pull must leave the local file alone and succeed: %v\n%s", err, out)
		}
		m.pushConfig()
		out, _ = m.status()
		wantState(t, out, "skills", "in step")
	})
	t.Run("local empty", func(t *testing.T) {
		m, f := scaffoldOnlyMachine(t)
		f.cfg.put("probe", "skills/team.md", []byte("team skill\n"))
		m.voidBase("skills", nil)
		out, _ := m.status()
		if cols := wantState(t, out, "skills", "behind"); cols[4] != "satelle sync pull" {
			t.Errorf("skills next = %q, want satelle sync pull", cols[4])
		}
		if out, err := m.pull(); err != nil {
			t.Fatalf("pull: %v\n%s", err, out)
		}
		out, _ = m.status()
		wantState(t, out, "skills", "in step")
	})
}
