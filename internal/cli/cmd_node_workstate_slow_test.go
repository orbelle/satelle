//go:build clislow && !windows

package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
)

// markNodeCalls stamps each Sync and Chain call the node makes with how many
// node-API calls the fake had answered by then, so a test can order the two.
func markNodeCalls(r *nodeRig) {
	setup := r.fakeSetup
	r.fakeSetup = func(f *devtest.Node) {
		if setup != nil {
			setup(f)
		}
		r.sync.rec.mark = func() int { return len(f.Calls()) }
	}
}

// firstRPC is the first recorded call of method (on story, when given).
func firstRPC(t *testing.T, r *nodeRig, method, story string) rpcCall {
	t.Helper()
	for _, c := range r.sync.rec.all() {
		if c.Method == method && (story == "" || c.Story == story) {
			return c
		}
	}
	t.Fatalf("no %s call (story %q); calls: %+v", method, story, r.sync.rec.all())
	return rpcCall{}
}

// A concluded task wake publishes its execution, the execution's own rows, the
// subject's link row and the run-output doc to the project its run descriptor
// names, under the node token, before the concluded heartbeat.
func TestSlowNodeTaskWakePushesWorkstate(t *testing.T) {
	r, subject := taskRig(t, "nightly-sweep", true)
	setup := r.fakeSetup
	r.fakeSetup = func(f *devtest.Node) {
		setup(f)
		f.Project = "acme-team"
	}
	markNodeCalls(r)
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}

	apply := onePush(t, r)
	if apply.GetProject() != "acme-team" {
		t.Errorf("applied to project %q, want the run descriptor's", apply.GetProject())
	}
	for _, c := range r.sync.rec.all() {
		if c.Auth != "Bearer "+w.spec.Token || c.Project != "acme-team" {
			t.Errorf("call %+v, want the node token in acme-team", c)
		}
	}
	exec := executionOf(t, apply)

	links := 0
	for _, row := range r.sync.appended(t, subject.ID()) {
		if row.Kind != ledger.KindExecutionLink {
			continue
		}
		links++
		var p ledger.ExecutionLinkPayload
		if err := json.Unmarshal(row.Payload, &p); err != nil {
			t.Fatal(err)
		}
		want := ledger.ExecutionLinkPayload{Execution: exec, Task: "tsk_nightly-sweep", Remote: w.spec.Session, Outcome: verb.OutcomeAccept}
		if p != want {
			t.Errorf("link row = %+v, want %+v", p, want)
		}
	}
	if links != 1 {
		t.Errorf("link rows appended to the subject = %d, want 1", links)
	}
	if own := r.sync.appended(t, exec); len(own) == 0 {
		t.Error("the execution's own rows were not appended to its chain")
	}

	if outs := apply.GetOutputs(); len(outs) != 1 || outs[0].GetExecution() != exec || outs[0].GetName() != "output-"+exec+".md" ||
		!strings.Contains(string(outs[0].GetBody()), "swept the lot") {
		t.Errorf("outputs = %v, want the run-output doc of %s", outs, exec)
	}

	// Published after the run and before it was reported concluded and the token ended.
	calls := w.fake.Calls()
	at := firstRPC(t, r, "Apply", "").At
	for _, c := range calls[:at] {
		if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/end") ||
			c.Method == http.MethodPut && strings.Contains(c.Body, `"run_state":"concluded"`) {
			t.Errorf("%s %s came before the push", c.Method, c.Path)
		}
	}
	var after []string
	for _, c := range calls[at:] {
		switch {
		case c.Method == http.MethodPut && strings.HasSuffix(c.Path, "/status"):
			after = append(after, "status")
		case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/end"):
			after = append(after, "end")
		}
	}
	if got := strings.Join(after, " "); !strings.HasSuffix(got, "status end") {
		t.Errorf("after the push: %s, want the concluded heartbeat, then end", got)
	}
}

// A task node whose root is marked pull-only (sty_07eefabe) runs its task and
// pushes no workstate, saying why.
func TestSlowPullOnlyNodeWorkstatePushSkipped(t *testing.T) {
	r, _ := taskRig(t, "nightly-sweep", true)
	writeRepoFile(t, r.nodeRoot, ".satelle/"+config.LocalConfigName, "[sync]\npull_only = true\n")
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if calls := r.sync.rec.all(); len(calls) != 0 {
		t.Errorf("work-state RPCs from a pull-only node = %+v, want none\nnode log:\n%s", calls, r.log)
	}
	if log := r.log.String(); !strings.Contains(log, "workstate push skipped") || !strings.Contains(log, "pull_only") {
		t.Errorf("node log does not report the skipped push naming pull_only:\n%s", log)
	}
}

// sty_0d621eaf AC1: a node woken for a story whose hosted chain already has events
// (its head a kill row, as nod_cc5ccae94eef's was) reads that chain under its node
// token before it makes or drives the execution, reads the execution's chain
// before driving it, and its push builds the link row on the hosted head: the fake
// judges each row's base as satelle.dev does, accepts it, and the link row
// becomes the story's hosted head.
func TestSlowNodeTaskWakePushBuildsOnHostedHead(t *testing.T) {
	const subject, kill = "sty_hosted01", "led_kill_nod_916075889ca3"
	r := hostedSubjectRig(t, subject,
		ledger.Entry{Kind: ledger.KindComment, Actor: "bob", Body: "made on the developer's machine"},
		ledger.Entry{ID: kill, Kind: ledger.KindComment, Actor: "satelle", Body: "an earlier node was killed"})
	markNodeCalls(r)
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	exec := executionOf(t, onePush(t, r))
	for _, c := range r.sync.rec.all() {
		if c.Auth != "Bearer "+w.spec.Token {
			t.Errorf("%s carried Authorization %q, want the node token", c.Method, c.Auth)
		}
	}

	// The subject's chain is read before the execution's, both before the drive,
	// and the push after it.
	drive := -1
	for i, c := range w.fake.Calls() {
		if c.Method == http.MethodPut && strings.HasSuffix(c.Path, "/status") && strings.Contains(c.Body, `"run_state":"running"`) {
			drive = i
			break
		}
	}
	if drive < 0 {
		t.Fatalf("no running heartbeat; calls: %+v", w.fake.Calls())
	}
	subjectRead, execRead, push := firstRPC(t, r, "Read", subject), firstRPC(t, r, "Read", exec), firstRPC(t, r, "Apply", "")
	if !(subjectRead.At <= execRead.At && execRead.At <= drive && push.At > drive) {
		t.Errorf("subject read at %d, execution read at %d, drive at %d, push at %d: want the reads before the drive and the push after",
			subjectRead.At, execRead.At, drive, push.At)
	}

	rows := r.sync.appended(t, subject)
	if len(rows) != 1 || rows[0].Kind != ledger.KindExecutionLink {
		t.Fatalf("subject rows pushed = %+v, want only this run's link row", rows)
	}
	if rows[0].BaseID != kill {
		t.Errorf("link row base = %q, want the hosted head %q", rows[0].BaseID, kill)
	}
	if chain := r.sync.f.chain.stored(devtest.DefaultProject, subject); len(chain) != 3 || chain[2] != rows[0].ID {
		t.Errorf("the subject's hosted chain is %v, want the two seeded and the link row %q", chain, rows[0].ID)
	}
	noStaleBase(t, r)
	if log := r.log.String(); !strings.Contains(log, "node: workstate: published to "+devtest.DefaultProject) {
		t.Errorf("the node's log does not say the push was published:\n%s", log)
	}
}

// sty_0d621eaf AC2: a node's run reaches the local machine. The developer's
// machine publishes a story; a node woken for it reads that story's hosted chain,
// runs and pushes to the same server and project; and the developer's next pull
// shows the run's execution_link, with its outcome, on the story's ledger, the
// chain unbroken.
func TestSlowNodeRunRowsReadableAfterPull(t *testing.T) {
	ts, f, _ := hostedModeRepo(t)
	subject := createStoryID(t, "Subject of a remote run")
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if len(f.chain.stored("probe", subject)) == 0 {
		t.Fatal("the developer's push left no hosted chain for the subject")
	}
	// The node is another machine: everything of the developer's is put back after it.
	local := map[string]string{}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "SATELLE_HOME", "SATELLE_CONFIG", "SATELLE_SERVER_ENDPOINT"} {
		local[k] = os.Getenv(k)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	r, _ := taskRig(t, "nightly-sweep", true)
	r.sync = serveNodeSync(t, f)
	r.fakeSetup = func(nf *devtest.Node) {
		nf.Run = map[string]string{"mode": "node", "story_id": subject, "trigger": "nightly", "trigger_source": "schedule"}
		nf.Project = "probe"
		nf.LostAfterSeconds = 3
	}
	useTaskNode(t, recordingPerformers(), 0)
	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	applies := 0
	for _, c := range r.sync.rec.all() {
		if c.Method == "Apply" {
			applies++
		}
	}
	if applies != 1 {
		t.Fatalf("the node made %d Sync/Apply calls, want one\nnode log:\n%s", applies, r.log)
	}
	all := r.sync.applies()
	exec := executionOf(t, all[len(all)-1])
	noStaleBase(t, r)
	// The execution's own rows, its recorded outcome among them, are on its hosted
	// chain beside the subject's link row.
	outcome := false
	for _, req := range f.chain.appendsFor(exec) {
		for _, ev := range req.GetEvents() {
			outcome = outcome || strings.Contains(string(ev.GetPayload()), "outcome "+verb.OutcomeAccept)
		}
	}
	if !outcome {
		t.Errorf("the execution's hosted chain carries no outcome row (%d events)", len(f.chain.stored("probe", exec)))
	}

	for k, v := range local {
		t.Setenv(k, v)
	}
	t.Chdir(cwd)
	if out, err := pullRaw(t, ts.URL); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	out, err := runRoot(t, "ledger", "list", "--story", subject, "--kind", ledger.KindExecutionLink)
	if err != nil {
		t.Fatalf("ledger list: %v\n%s", err, out)
	}
	for _, want := range []string{exec, "tsk_nightly-sweep", w.spec.Session, verb.OutcomeAccept} {
		if !strings.Contains(out, want) {
			t.Errorf("the subject's execution_link after the pull does not name %q:\n%s", want, out)
		}
	}
	if out, err := runRoot(t, "ledger", "verify", "--story", subject); err != nil || !strings.Contains(out, "chain ok") {
		t.Errorf("the subject's chain after the pull: %v\n%s", err, out)
	}
}

// The push needs no satelle login: the node's home holds no stored credentials and
// no [sync] project is bound, yet the run is pushed, and every request either
// server saw carried only the node token.
func TestSlowNodeWorkstatePushNeedsNoLogin(t *testing.T) {
	r, _ := taskRig(t, "nightly-sweep", true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	onePush(t, r)
	for _, c := range w.fake.Calls() {
		if c.Auth != "Bearer "+w.spec.Token {
			t.Errorf("%s %s carried Authorization %q, want only the node token", c.Method, c.Path, c.Auth)
		}
	}
	for _, c := range r.sync.rec.all() {
		if c.Auth != "Bearer "+w.spec.Token {
			t.Errorf("%s carried Authorization %q, want only the node token", c.Method, c.Auth)
		}
	}
	// Nothing of a login was stored in the empty home, and the node's own config
	// binds no project.
	if p, err := hosted.CredentialsPath(); err != nil {
		t.Fatal(err)
	} else if _, err := os.Stat(p); err == nil {
		t.Errorf("the node stored credentials at %s", p)
	}
	if cfg, err := os.ReadFile(filepath.Join(r.nodeRoot, ".satelle", "satelle.toml")); err == nil {
		for _, ln := range strings.Split(string(cfg), "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "project") {
				t.Errorf("the node's config binds a project: %q", ln)
			}
		}
	}
}

// sty_0d621eaf AC1: the subject's hosted chain already carries an earlier run's
// execution_link. The fetch brings it into the node's store, and the push sends
// only this run's link row, built on it, never the fetched one again.
func TestSlowNodeTaskWakeDoesNotRepushFetchedLink(t *testing.T) {
	const subject = "sty_hosted03"
	earlier, err := json.Marshal(ledger.ExecutionLinkPayload{Execution: "exe_earlier01", Task: "tsk_nightly-sweep", Remote: "nod_earlier", Outcome: verb.OutcomeAccept})
	if err != nil {
		t.Fatal(err)
	}
	const earlierID = "evt_earlierlink"
	r := hostedSubjectRig(t, subject,
		ledger.Entry{Kind: ledger.KindComment, Actor: "bob", Body: "raised"},
		ledger.Entry{ID: earlierID, Kind: ledger.KindExecutionLink, Actor: "remote-node", Payload: earlier})
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	exec := executionOf(t, onePush(t, r))

	rows := r.sync.appended(t, subject)
	if len(rows) != 1 || rows[0].Kind != ledger.KindExecutionLink {
		t.Fatalf("subject rows pushed = %+v, want exactly this run's link row", rows)
	}
	var p ledger.ExecutionLinkPayload
	if err := json.Unmarshal(rows[0].Payload, &p); err != nil || p.Execution != exec {
		t.Errorf("the pushed link names execution %q (%v), want this run's %q", p.Execution, err, exec)
	}
	if rows[0].BaseID != earlierID {
		t.Errorf("link row base = %q, want the earlier run's link %q", rows[0].BaseID, earlierID)
	}
	noStaleBase(t, r)
}

// sty_0d621eaf: a server with no Chain/Read does not block the run. The node says
// the chain was not read, writes on what its store holds, concludes and still
// pushes.
func TestSlowNodeTaskWakeChainReadMissingStillPushes(t *testing.T) {
	const subject = "sty_hosted04"
	r := hostedSubjectRig(t, subject)
	r.sync.f.chain.unreadable = true
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	log := r.log.String()
	for _, want := range []string{"the hosted chain of " + subject + " was not read before this write", hosted.ErrChainUnsupported.Error()} {
		if !strings.Contains(log, want) {
			t.Errorf("the node's log does not say %q:\n%s", want, log)
		}
	}
	onePush(t, r)
	st := w.fake.Statuses()
	if last := st[len(st)-1]; last["run_state"] != "concluded" || last["outcome"] != verb.OutcomeAccept {
		t.Errorf("last heartbeat = %+v, want concluded/accept", last)
	}
}
