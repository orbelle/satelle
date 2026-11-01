package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/snapsync"
)

const (
	holderID = "usr_holder"
	memberID = "usr_member"
)

func holderOf(id string) hosted.SubstrateHolder {
	return hosted.SubstrateHolder{UserID: id, Email: id + "@example.com"}
}

// fakeHolderServer is the combined sync fake with the project's substrate
// holder (sty_8c53b31c's contract): GET /api/v1/projects/{p} reports the
// holder, and a PUT of a path the server hosts is refused with a 403 naming the
// holder unless the bearer is the holder's. Every config PUT is counted by path.
type fakeHolderServer struct {
	*fakeSyncServer
	mu sync.Mutex
	// reported is the holder GET names; enforced is the one the PUT check admits.
	reported, enforced hosted.SubstrateHolder
	// projectStatus and manifestStatus, when set, are what those GETs answer.
	projectStatus, manifestStatus int
	// source and repo, when source is set, are the substrate the project GET
	// reports in place of the hosted one with its holder.
	source, repo string
	puts         map[string]int
}

// serverHostedPath is the server's hosted list: what only the holder may PUT.
func serverHostedPath(p string) bool {
	for _, prefix := range []string{"workflows/", "skills/", "principles/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return p == "constitution.md" || p == config.ConfigName
}

func newFakeHolderServer(t *testing.T, holder string) (*httptest.Server, *fakeHolderServer) {
	t.Helper()
	t.Setenv(hosted.SessionTokenEnv, "")
	base, f := newFakeSyncServer(t)
	h := &fakeHolderServer{fakeSyncServer: f, reported: holderOf(holder), enforced: holderOf(holder), puts: map[string]int{}}
	inner := base.Config.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/v1/projects/"), "/", 3)
		h.mu.Lock()
		reported, enforced, ps, ms := h.reported, h.enforced, h.projectStatus, h.manifestStatus
		substrate := map[string]any{"source": "hosted", "holder": reported}
		if h.source != "" {
			substrate = map[string]any{"source": h.source, "repo": h.repo}
		}
		h.mu.Unlock()
		switch {
		case len(segs) == 1 && r.Method == http.MethodGet:
			if ps != 0 {
				w.WriteHeader(ps)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "p1", "slug": segs[0], "name": "Probe", "team_id": "t1",
				"substrate": substrate,
			})
			return
		case len(segs) == 2 && segs[1] == "config" && r.Method == http.MethodGet && ms != 0:
			w.WriteHeader(ms)
			return
		case len(segs) == 3 && segs[1] == "config" && r.Method == http.MethodPut:
			h.mu.Lock()
			h.puts[segs[2]]++
			h.mu.Unlock()
			if serverHostedPath(segs[2]) && strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != enforced.UserID {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "not_holder", "holder": enforced})
				return
			}
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, h
}

func (h *fakeHolderServer) set(fn func(h *fakeHolderServer)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *fakeHolderServer) putsOf(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.puts[path]
}

// teamPuts is every PUT of a path the default team set owns.
func (h *fakeHolderServer) teamPuts() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	team := config.NewTeamSet(nil)
	out := map[string]int{}
	for p, n := range h.puts {
		if team.Owns(p) {
			out[p] = n
		}
	}
	return out
}

// version is the number of hosted versions of path.
func (h *fakeHolderServer) version(path string) int {
	h.cfg.mu.Lock()
	defer h.cfg.mu.Unlock()
	return len(h.cfg.data["probe"][path])
}

func (h *fakeHolderServer) head(path string) string {
	b, _, _ := h.cfg.get("probe", path, 0)
	return string(b)
}

// seedCredAs stores a credential for server whose principal (and bearer, which
// the fake reads as the caller) is id.
func seedCredAs(t *testing.T, server, id string) {
	t.Helper()
	if err := (hosted.FileStore{}).Save(hosted.Credential{ServerURL: server, AccessToken: id, RefreshToken: "r", PrincipalID: id, Email: id + "@example.com"}); err != nil {
		t.Fatal(err)
	}
}

const memberTask = "tasks/tsk_member.md"

func memberTaskBody(v string) string {
	return "---\nid: tsk_member\ntype: task\n---\n# member task " + v + "\n"
}

// holderTierRepo is a bound repo with every area but the ledger personal (the
// fake serves no story chain), whose team-owned files and a task the holder
// pushed and deployed as its project tier. The stored credential is the
// member's, and no PUT is counted yet.
func holderTierRepo(t *testing.T) (string, *httptest.Server, *fakeHolderServer) {
	t.Helper()
	return holderTierRepoWith(t, nil)
}

// holderTierRepoWith is holderTierRepo with prep run on the repo before the
// holder's push, so what it writes is part of the deployed tier. A nil prep
// does nothing.
func holderTierRepoWith(t *testing.T, prep func(t *testing.T, repo string)) (string, *httptest.Server, *fakeHolderServer) {
	t.Helper()
	ts, h := newFakeHolderServer(t, holderID)
	seedCredAs(t, ts.URL, holderID)
	repo := agentsPushRepo(t, "[sync]\n"+boundProjectToml, tierAgents("team-v1"))
	if prep != nil {
		prep(t, repo)
	}
	writeRepoFile(t, repo, ".satelle/skills/team-skill.md", tierSkill("v1"))
	writeRepoFile(t, repo, ".satelle/"+memberTask, memberTaskBody("v1"))
	tierPush(t, ts.URL)
	tierDeploy(t, ts.URL)
	h.set(func(h *fakeHolderServer) { h.puts = map[string]int{} })
	seedCredAs(t, ts.URL, memberID)
	return repo, ts, h
}

// AC1: the holder's config push sends the changed team-owned file and the
// server records a new version of it.
func TestHolderPublishCreatesVersion(t *testing.T) {
	repo, ts, h := holderTierRepo(t)
	seedCredAs(t, ts.URL, holderID)
	writeRepoFile(t, repo, ".satelle/skills/team-skill.md", tierSkill("v2"))

	out, err := runRoot(t, "sync", "config", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("holder push: %v\n%s", err, out)
	}
	if n := h.putsOf("skills/team-skill.md"); n != 1 {
		t.Errorf("PUTs of skills/team-skill.md = %d, want 1\n%s", n, out)
	}
	if v := h.version("skills/team-skill.md"); v != 2 || h.head("skills/team-skill.md") != tierSkill("v2") {
		t.Errorf("hosted version %d = %q, want version 2 with the holder's bytes", v, h.head("skills/team-skill.md"))
	}
	if strings.Contains(out, "team substrate not published") {
		t.Errorf("the holder was refused:\n%s", out)
	}
}

// AC2: a member's config push of an edited team-owned file exits non-zero
// naming the holder; the path gets no PUT and no version, and a non-team file
// in the same run is still pushed.
func TestMemberPublishRefusedNamesHolder(t *testing.T) {
	repo, ts, h := holderTierRepo(t)
	writeRepoFile(t, repo, ".satelle/skills/team-skill.md", tierSkill("member-edit"))
	writeRepoFile(t, repo, ".satelle/"+memberTask, memberTaskBody("v2"))

	out, err := runRoot(t, "sync", "config", "push", "--server", ts.URL)
	if err == nil {
		t.Fatalf("a member's publish was not refused:\n%s", out)
	}
	for _, want := range []string{holderOf(holderID).Email, "skills/team-skill.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q must name %s", err, want)
		}
	}
	if n := h.putsOf("skills/team-skill.md"); n != 0 {
		t.Errorf("PUTs of skills/team-skill.md = %d, want 0", n)
	}
	if v := h.version("skills/team-skill.md"); v != 1 || h.head("skills/team-skill.md") != tierSkill("v1") {
		t.Errorf("hosted team-skill.md moved to version %d", v)
	}
	if h.head(memberTask) != memberTaskBody("v2") {
		t.Errorf("the member's task was not pushed:\n%s", out)
	}
}

// AC2: the server's own 403 (the holder changed between the lookup and the PUT)
// names the holder from its body, and no version is created.
func TestMemberServerRefusalNamesHolder(t *testing.T) {
	repo, ts, h := holderTierRepo(t)
	h.set(func(h *fakeHolderServer) { h.reported = holderOf(memberID) })
	writeRepoFile(t, repo, ".satelle/skills/team-skill.md", tierSkill("member-edit"))

	out, err := runRoot(t, "sync", "config", "push", "--server", ts.URL)
	if err == nil {
		t.Fatalf("the server's refusal did not fail the push:\n%s", out)
	}
	if !strings.Contains(err.Error(), holderOf(holderID).Email) || !strings.Contains(err.Error(), "skills/team-skill.md") {
		t.Errorf("refusal %q must name the 403's holder and the path", err)
	}
	if v := h.version("skills/team-skill.md"); v != 1 {
		t.Errorf("hosted team-skill.md moved to version %d", v)
	}
}

// AC2: with the manifest unreadable the push falls back to the plain upload,
// which still sends no team-owned path for a member.
func TestMemberPublishManifestFailureStillGated(t *testing.T) {
	repo, ts, h := holderTierRepo(t)
	h.set(func(h *fakeHolderServer) { h.manifestStatus = http.StatusInternalServerError })
	writeRepoFile(t, repo, ".satelle/skills/team-skill.md", tierSkill("member-edit"))
	writeRepoFile(t, repo, ".satelle/"+memberTask, memberTaskBody("v2"))

	out, err := runRoot(t, "sync", "config", "push", "--server", ts.URL)
	if err == nil || !strings.Contains(err.Error(), holderOf(holderID).Email) {
		t.Fatalf("push err = %v, want a refusal naming the holder\n%s", err, out)
	}
	if puts := h.teamPuts(); len(puts) != 0 {
		t.Errorf("team-owned PUTs = %v, want none", puts)
	}
	if h.head(memberTask) != memberTaskBody("v2") {
		t.Errorf("the member's task was not pushed:\n%s", out)
	}
}

// AC3: a member's bare sync sends no team-owned path — not a backup-failed
// edit, not a new file — and the server refuses the member's satelle.toml; it
// exits 0 with one warning naming the holder and every withheld file, the
// skills snapshot still names the project's version, and the member's task,
// document and work-state still push.
func TestMemberBareSyncNeverSendsTeamFiles(t *testing.T) {
	repo, ts, h := holderTierRepo(t)
	if err := os.WriteFile(filepath.Join(runtimeDirOf(t, repo), "backups"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, repo, ".satelle/skills/team-skill.md", tierSkill("member-edit"))
	writeRepoFile(t, repo, ".satelle/skills/member-new.md", "---\ntype: skill\nname: member-new\ndescription: mine\n---\n# mine\n")
	writeRepoFile(t, repo, ".satelle/satelle.toml", readRepoFile(t, repo, ".satelle/satelle.toml")+"\n# member edit\n")
	writeRepoFile(t, repo, ".satelle/"+memberTask, memberTaskBody("v2"))
	writeRepoFile(t, repo, ".satelle/documents/member-doc.md", "# member doc\n")
	if out, err := runRoot(t, "story", "create", "--title", "Member probe", "--body", "x", "--acceptance", "1. ok"); err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	settingsVersion := h.version(config.ConfigName)

	stdout, stderr, err := runRootSplit(t, "", "sync", "--server", ts.URL)
	if err != nil {
		t.Fatalf("bare sync must exit 0: %v\n%s%s", err, stdout, stderr)
	}
	if readRepoFile(t, repo, ".satelle/skills/team-skill.md") != tierSkill("member-edit") {
		t.Fatal("setup: the edit must survive the write-down for the push to see it")
	}
	assertOneHolderWarning(t, stderr, "skills/team-skill.md", "skills/member-new.md", config.ConfigName)
	for _, p := range []string{"skills/team-skill.md", "skills/member-new.md"} {
		if n := h.putsOf(p); n != 0 {
			t.Errorf("PUTs of %s = %d, want 0", p, n)
		}
	}
	if v := h.version(config.ConfigName); v != settingsVersion {
		t.Errorf("hosted satelle.toml moved from version %d to %d", settingsVersion, v)
	}
	body, _, ok := h.cfg.get("probe", snapsync.RecordPath("skills"), 0)
	if !ok {
		t.Fatal("no hosted skills snapshot")
	}
	rec, err := snapsync.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Files["skills/team-skill.md"]; got != sha256hex([]byte(tierSkill("v1"))) {
		t.Errorf("skills snapshot names team-skill.md at %q, want the project's version", got)
	}
	if _, named := rec.Files["skills/member-new.md"]; named {
		t.Error("skills snapshot names the member's new file")
	}
	assertMemberLegsPushed(t, h, stdout+stderr)
}

// AC3: with the manifest unreadable, a member's bare sync still sends no
// team-owned path, exits 0 with the warning, and pushes the other legs.
func TestMemberBareSyncManifestFailure(t *testing.T) {
	repo, ts, h := holderTierRepo(t)
	h.set(func(h *fakeHolderServer) { h.manifestStatus = http.StatusInternalServerError })
	writeRepoFile(t, repo, ".satelle/"+memberTask, memberTaskBody("v2"))
	writeRepoFile(t, repo, ".satelle/documents/member-doc.md", "# member doc\n")
	if out, err := runRoot(t, "story", "create", "--title", "Member probe", "--body", "x", "--acceptance", "1. ok"); err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}

	stdout, stderr, err := runRootSplit(t, "", "sync", "--server", ts.URL)
	if err != nil {
		t.Fatalf("bare sync must exit 0: %v\n%s%s", err, stdout, stderr)
	}
	if puts := h.teamPuts(); len(puts) != 0 {
		t.Errorf("team-owned PUTs = %v, want none", puts)
	}
	assertOneHolderWarning(t, stderr, "skills/team-skill.md")
	assertMemberLegsPushed(t, h, stdout+stderr)
}

// AC3: a project lookup that fails while a tier is present gates as for a
// member, and the refusal names the failed lookup.
func TestMemberGateFailsClosed(t *testing.T) {
	repo, ts, h := holderTierRepo(t)
	h.set(func(h *fakeHolderServer) { h.projectStatus = http.StatusInternalServerError })
	writeRepoFile(t, repo, ".satelle/skills/team-skill.md", tierSkill("member-edit"))

	out, err := runRoot(t, "sync", "config", "push", "--server", ts.URL)
	if err == nil || !strings.Contains(err.Error(), "project lookup failed") {
		t.Fatalf("push err = %v, want a refusal naming the failed lookup\n%s", err, out)
	}
	if n := h.putsOf("skills/team-skill.md"); n != 0 {
		t.Errorf("PUTs of skills/team-skill.md = %d, want 0", n)
	}
}

func assertOneHolderWarning(t *testing.T, stderr string, paths ...string) {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.Contains(l, "team substrate not published") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "warning: ") {
		t.Fatalf("want exactly one holder warning, got %d:\n%s", len(lines), stderr)
	}
	for _, want := range append([]string{holderOf(holderID).Email}, paths...) {
		if !strings.Contains(lines[0], want) {
			t.Errorf("warning must name %s: %s", want, lines[0])
		}
	}
}

func assertMemberLegsPushed(t *testing.T, h *fakeHolderServer, out string) {
	t.Helper()
	if h.head(memberTask) != memberTaskBody("v2") {
		t.Errorf("the member's task was not pushed:\n%s", out)
	}
	if _, _, ok := h.docs.get("probe", "documents/member-doc.md"); !ok {
		t.Errorf("the documents leg did not push:\n%s", out)
	}
	h.fakeSyncServer.mu.Lock()
	posts := h.workstate
	h.fakeSyncServer.mu.Unlock()
	if posts == 0 {
		t.Errorf("the work-state leg did not push:\n%s", out)
	}
}
