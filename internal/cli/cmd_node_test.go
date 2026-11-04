//go:build !windows

package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/remotechan/relaytest"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/remotenode/gittest"
	"github.com/bobmcallan/satelle/internal/wfgovern"
)

type nodeTestStore struct{ cred hosted.Credential }

func (m *nodeTestStore) Load(string) (hosted.Credential, error) { return m.cred, nil }
func (m *nodeTestStore) Save(c hosted.Credential) error         { m.cred = c; return nil }
func (m *nodeTestStore) Delete(string) error                    { return nil }

// nodeLog is the node's stderr, readable while the node still writes it.
type nodeLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *nodeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *nodeLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// nodeDefinition is the substrate files of a node's definition: the directories
// remotenode.SubstrateDirs names, a one-lane workflow, one binding that runs the
// fake agent, one skill and one principle. The definition's remote.toml is the
// v1 story fixture in remotedef's testdata.
var nodeDefinition = map[string]string{
	"workflows/done.toml": `[meta]
name = "done"
type = "workflow"
scope = "project"
tags = ["type:workflow"]
description = "The node's declaration of done."

["*"]
obligations = ["raised", "coded", "closed"]
`,
	"workflows/step.toml": `[meta]
name = "step"
type = "workflow"
scope = "project"
tags = ["type:workflow"]
description = "The node's step catalogue."

[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "worker"
skills = ["remote-work"]
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`,
	"workflows/agents.toml": "[worker]\nrole = \"agent\"\ncommand = \"fake-agent --system {system} --tools {tools}\"\ntools = \"read_file\"\n",
	"skills/remote-work.md": `---
name: remote-work
type: skill
scope: project
tags: [type:skill]
description: The rubric the node's worker follows when it performs the coded step.
---

# Remote work

Implement the work item you are given.
`,
	"principles/remote-conduct.md": `---
name: remote-conduct
type: principle
tags: [principles:session]
description: How an agent on this node conducts itself.
---

# Remote conduct

Work only inside the story you were handed.
`,
}

// nodeRig is one node machine: its environment, the relay and the satelle.dev fake
// it talks to.
type nodeRig struct {
	t        *testing.T
	relay    *relaytest.Relay
	log      *nodeLog
	nodeRoot string
	marker   string // the fake agent appends a line here each time it runs
	repo     *gittest.Server
	bundle   map[string]string
	// tokenStdin makes join hand over the token on stdin (--token-stdin) rather
	// than in argv.
	tokenStdin bool
	// fakeSetup, when set, configures the satelle.dev fake as serve creates it,
	// before the node asks it anything.
	fakeSetup func(*devtest.Node)
	// sync is the Sync and Chain services the node's work state reaches.
	sync *nodeSync

	// joinCtx is the context every join in this rig runs under, and joins counts
	// the joins still running. A node redials a dropped relay for its resume window,
	// so a join whose test ends without ending it would run on past the test, into
	// its cleanup; the rig's cleanup cancels joinCtx and waits for the joins instead.
	joinCtx   context.Context
	stopJoins context.CancelFunc
	joins     sync.WaitGroup
}

// newNodeRig sets the environment of a fresh machine with a fake agent on its PATH
// and the seams pointing at in-process fakes. The definition it will serve is
// nodeDefinition plus the v1 story fixture as its remote.toml.
func newNodeRig(t *testing.T) *nodeRig {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("the fake agent is a shell script")
	}
	r := &nodeRig{t: t, log: &nodeLog{}, relay: relaytest.New(t), repo: gittest.New(t)}
	r.joinCtx, r.stopJoins = context.WithCancel(context.Background())
	// Read before the working directory changes: it is the v1 story fixture,
	// committed with the remotedef package.
	remoteToml, err := os.ReadFile(filepath.Join("..", "remotedef", "testdata", "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	r.marker = filepath.Join(t.TempDir(), "agent-ran")
	script := "#!/bin/sh\necho ran >> " + r.marker + "\necho \"TOKEN=$CLAUDE_CODE_OAUTH_TOKEN\"\ncat\n"
	if err := os.WriteFile(filepath.Join(bin, "fake-agent"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The node is a fresh machine: nothing of this repo's, nor of the developer's.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv("SATELLE_CONFIG", "")
	t.Setenv("SATELLE_SERVER_ENDPOINT", "none")
	t.Setenv(config.SessionEnv, "")
	// join changes into the node root; the cleanup puts the working directory back.
	t.Chdir(t.TempDir())
	r.nodeRoot = filepath.Join(t.TempDir(), "satelle-node")

	prevOpts, prevServer, prevHTTP, prevEnv := nodeChannelOptions, nodeServer, nodeHTTP, nodeFetchEnv
	nodeChannelOptions = []remotechan.Option{remotechan.WithHTTPClient(r.relay.Client())}
	// The fixture's repo is on github.com; the node's git is told the local server is it.
	nodeFetchEnv = append(append([]string(nil), r.repo.Env...),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url."+r.repo.URL+".insteadOf",
		"GIT_CONFIG_VALUE_0=https://github.com/acme/widgets.git")
	t.Cleanup(func() {
		nodeChannelOptions, nodeServer, nodeHTTP, nodeFetchEnv = prevOpts, prevServer, prevHTTP, prevEnv
	})

	r.bundle = map[string]string{"remote.toml": string(remoteToml)}
	for rel, body := range nodeDefinition {
		r.bundle[rel] = body
	}
	// The fixture's setup names a host-only command the test node does not
	// have; a rig starts without one, and a test that wants one sets it.
	r.setSetup(nil, nil)
	r.sync = newNodeSync(t)
	// Registered last, so it runs first: before the relay closes its connections
	// (which a node would answer by redialling), before the temp directories the
	// node works in go, and before the seams above are put back.
	t.Cleanup(r.endJoins)
	return r
}

// endJoins stops every join the rig started and waits for each to return, so none
// outlives its test.
func (r *nodeRig) endJoins() {
	r.stopJoins()
	done := make(chan struct{})
	go func() { r.joins.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		r.t.Error("a node join did not stop when its test ended")
	}
}

// encode is the stored bundle: the rig's files as a format-1 bundle, in path order.
func (r *nodeRig) encode() []byte {
	r.t.Helper()
	paths := make([]string, 0, len(r.bundle))
	for p := range r.bundle {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	entries := make([]filebundle.Entry, len(paths))
	for i, p := range paths {
		entries[i] = filebundle.Entry{Path: p, Content: r.bundle[p]}
	}
	b, err := filebundle.Encode(entries)
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

// serve points the node at a satelle.dev fake for node id.
func (r *nodeRig) serve(id, token string, bundle []byte) *devtest.Node {
	r.t.Helper()
	fake := devtest.NewNode(r.t, id, token, bundle, "v7")
	fake.Secrets["CLAUDE_CODE_OAUTH_TOKEN"] = devtest.SecretValue
	if r.fakeSetup != nil {
		r.fakeSetup(fake)
	}
	nodeServer = func() string { return fake.URL }
	nodeHTTP = fake.Client()
	return fake
}

// join runs `satelle node join` in this process and returns its error.
func (r *nodeRig) join(stdout io.Writer, relayURL, session, token string) error {
	r.joins.Add(1)
	defer r.joins.Done()
	root := NewRootCmd()
	resetFlagState(root)
	root.SetContext(r.joinCtx)
	root.SetOut(stdout)
	root.SetErr(r.log)
	args := []string{"node", "join", "--root", r.nodeRoot, "--relay", relayURL, "--session", session}
	if r.tokenStdin {
		// The writer sends the token and a newline and is never closed while the
		// node runs, as a server's exec stdin stream is not.
		pr, pw := io.Pipe()
		defer pw.Close()
		go func() { _, _ = pw.Write([]byte(token + "\n")) }()
		root.SetIn(pr)
		args = append(args, "--token-stdin")
	} else {
		args = append(args, "--token", token)
	}
	root.SetArgs(args)
	return executeRoot(root)
}

// joinNow runs join to its end with nothing reading the key line.
func (r *nodeRig) joinNow(relayURL, session, token string) error {
	return r.join(io.Discard, relayURL, session, token)
}

// launcher plays satelle-server's launcher: once Open has issued the session and
// the token it starts the satelle.dev fake for that node, keyed by the session as
// the real server keys it, and starts `satelle node join` against it.
func (r *nodeRig) launcher(fakeCh chan<- *devtest.Node, joined chan<- error) remotechan.Launcher {
	return remotechan.LauncherFunc(func(_ context.Context, spec remotechan.LaunchSpec) ([]byte, error) {
		fakeCh <- r.serve(spec.Session, spec.Token, r.encode())
		pr, pw := io.Pipe()
		go func() {
			err := r.join(pw, spec.RelayURL, spec.Session, spec.Token)
			_ = pw.CloseWithError(err)
			joined <- err
		}()
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), remotenode.KeyLinePrefix); ok {
				go func() { _, _ = io.Copy(io.Discard, pr) }()
				return base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
			}
		}
		return nil, errors.New("node join printed no key line:\n" + r.log.String())
	})
}

// open brings a node up through the relay and returns the controller's run.
func (r *nodeRig) open(t *testing.T) (run *remotenode.Run, fake *devtest.Node, joined chan error, spec remotechan.LaunchSpec) {
	t.Helper()
	run, fake, joined, spec, err := r.tryOpen(t)
	if err != nil {
		t.Fatalf("Attach: %v\nnode log:\n%s", err, r.log)
	}
	return run, fake, joined, spec
}

// tryOpen is open that returns the controller's Attach error (a node that is not
// ready) instead of failing the test.
func (r *nodeRig) tryOpen(t *testing.T) (run *remotenode.Run, fake *devtest.Node, joined chan error, spec remotechan.LaunchSpec, attachErr error) {
	t.Helper()
	fakeCh := make(chan *devtest.Node, 1)
	joined = make(chan error, 1)
	client := hosted.NewClient("https://satelle.test", &nodeTestStore{cred: hosted.Credential{ServerURL: "https://satelle.test", AccessToken: "access-1"}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	launch := r.launcher(fakeCh, joined)
	var got remotechan.LaunchSpec
	sess, err := remotechan.Open(ctx, client, r.relay.URL(), remotechan.LauncherFunc(func(c context.Context, s remotechan.LaunchSpec) ([]byte, error) {
		got = s
		return launch.Launch(c, s)
	}), remotechan.WithHTTPClient(r.relay.Client()))
	if err != nil {
		t.Fatalf("Open: %v\nnode log:\n%s", err, r.log)
	}
	run, err = remotenode.Attach(ctx, sess, 60*time.Second)
	return run, <-fakeCh, joined, got, err
}

// taskWake is a node satelle.dev woke: its fake, the join's end, the launch it was
// handed, and the relay it was pointed at, which counts every request and pairs
// nothing.
type taskWake struct {
	fake      *devtest.Node
	joined    chan error
	spec      remotechan.LaunchSpec
	relayHits *atomic.Int32
}

// wake plays satelle.dev waking a task node from a route or trigger: it serves the
// node's fake and starts `satelle node join` with no controller end on the relay,
// pointed at a relay that never upgrades, so a node that waited on the relay would
// never pair.
func (r *nodeRig) wake(t *testing.T) taskWake {
	t.Helper()
	w := taskWake{joined: make(chan error, 1), relayHits: new(atomic.Int32)}
	relay := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		w.relayHits.Add(1)
		rw.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(relay.Close)
	w.spec = remotechan.LaunchSpec{RelayURL: relay.URL, Session: "nod_wake", Token: "node-token"}
	w.fake = r.serve(w.spec.Session, w.spec.Token, r.encode())
	go func() { w.joined <- r.joinNow(w.spec.RelayURL, w.spec.Session, w.spec.Token) }()
	return w
}

// files is every file under dir, by slash path.
func nodeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func waitJoin(t *testing.T, joined <-chan error) error {
	t.Helper()
	select {
	case err := <-joined:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("node join did not end")
		return nil
	}
}

// `satelle node join` is the node mode that stays in the binary: handed the launch
// contract (--root, --relay, --session, --token) it prints its key line, fetches
// its definition from satelle.dev with its node token, installs and reindexes it,
// reads its secrets from satelle.dev, joins the relay with the same token, reports
// ready, answers an agent job with those secrets, and ends itself through
// satelle.dev. The
// controller end is remotenode.Attach.
func TestNodeJoinRunsFromTheDefinitionItFetches(t *testing.T) {
	r := newNodeRig(t)
	run, fake, joined, spec := r.open(t)

	// AC1: the definition came from the fake with the node bearer, under the
	// session as node id, and the report carries the version header.
	calls := nonStatusCalls(fake)
	if len(calls) == 0 || calls[0].Method != http.MethodGet || calls[0].Path != "/api/v1/remote-nodes/"+spec.Session+"/definition" ||
		calls[0].Auth != "Bearer "+spec.Token {
		t.Fatalf("first call = %+v, want the authenticated definition fetch", calls)
	}
	rep := run.Report
	if rep.Error != "" || rep.Version == "" || rep.DefinitionVersion != "v7" {
		t.Errorf("report = %+v", rep)
	}
	// The workflows are the effective route's: the bundle's done.toml merged by
	// name over the embedded default.
	wantWorkflows := categoriesOf(t, wfgovern.RouteSourceOf([]docindex.Doc{{Kind: "workflows", Name: wfgovern.RouteSourceDone, Body: nodeDefinition["workflows/done.toml"]}}).Done)
	if !reflect.DeepEqual(rep.Bindings, []string{"worker"}) || !reflect.DeepEqual(rep.Workflows, wantWorkflows) {
		t.Errorf("report bindings/workflows = %v / %v, want [worker] / %v", rep.Bindings, rep.Workflows, wantWorkflows)
	}
	// The report's allowlist and repos are what remote.toml declares.
	if want := []string{"api.example.com", "*.example.org"}; !reflect.DeepEqual(rep.Allow, want) {
		t.Errorf("report allow = %v, want %v", rep.Allow, want)
	}
	if len(rep.Repos) != 1 || rep.Repos[0].Name != "widgets" || rep.Repos[0].Commit != r.repo.Head || rep.Repos[0].Ref != "main" {
		t.Errorf("report repos = %+v, want widgets at %s", rep.Repos, r.repo.Head)
	}

	// The installed substrate is the served substrate, and remote.toml is not installed.
	dataDir := filepath.Join(r.nodeRoot, ".satelle")
	for rel, want := range nodeDefinition {
		got, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("the node lacks %s: %v", rel, err)
		} else if string(got) != want {
			t.Errorf("the node's %s is not the definition's", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "remote.toml")); err == nil {
		t.Error("remote.toml was installed on the node")
	}
	// Nothing secret was written to the node.
	if _, err := os.Stat(filepath.Join(dataDir, "config", ".env")); err == nil {
		t.Error("a .env was written to the node")
	}

	// AC9: the relay saw the node token as the remote's bearer.
	if got := r.relay.RemoteBearers(); !reflect.DeepEqual(got, []string{spec.Token}) {
		t.Errorf("relay remote bearers = %v, want [the node token]", got)
	}

	// AC5: join read the declared secret from satelle.dev with the node bearer,
	// once, and the agent job hands it to the agent's environment without a read of
	// its own (sty_2dabc0f6).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	body, err := run.Request(ctx, remotenode.AgentJob, remotenode.AgentRequest{Text: "hello node"})
	if err != nil {
		t.Fatalf("agent job: %v\nnode log:\n%s", err, r.log)
	}
	reply, err := remotenode.DecodeAgentReply(body)
	if err != nil || !strings.Contains(reply.Text, "hello node") || !strings.Contains(reply.Text, "TOKEN="+devtest.SecretValue) {
		t.Errorf("agent reply = %+v, %v", reply, err)
	}
	var secretCalls []devtest.Call
	for _, c := range fake.Calls() {
		if strings.Contains(c.Path, "/secrets/") {
			secretCalls = append(secretCalls, c)
		}
	}
	if len(secretCalls) != 1 || secretCalls[0].Method != http.MethodPost || secretCalls[0].Auth != "Bearer "+spec.Token ||
		secretCalls[0].Path != "/api/v1/remote-nodes/"+spec.Session+"/secrets/CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("secret calls = %+v", secretCalls)
	}

	// Closing the channel ends the node's join cleanly, and the node ends itself
	// through satelle.dev exactly once.
	if err := run.End(); err != nil {
		t.Errorf("End: %v", err)
	}
	if err := waitJoin(t, joined); err != nil {
		t.Errorf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	if n := fake.Count(http.MethodGet, "/definition"); n != 1 {
		t.Errorf("definition fetches = %d, want 1", n)
	}

	// The secret value is in no file of the node and not in its log.
	for rel, body := range nodeFiles(t, r.nodeRoot) {
		if strings.Contains(body, devtest.SecretValue) {
			t.Errorf("the secret value was written to %s", rel)
		}
	}
	if strings.Contains(r.log.String(), devtest.SecretValue) {
		t.Error("the secret value is in the node's log")
	}
}

// repoBlocks matches the [[repo]] entries of the v1 fixture.
var repoBlocks = regexp.MustCompile(`(?m)^\[\[repo\]\]\nurl = .*\nref = .*\n`)

// setRepos makes the definition the rig serves declare repos as its [[repo]]
// entries, each served by the rig's git server at the URL its served map names;
// nil declares none.
func (r *nodeRig) setRepos(repos []remotedef.Repo, served map[string]string) {
	r.t.Helper()
	cur := r.bundle["remote.toml"]
	if !repoBlocks.MatchString(cur) {
		r.t.Fatal("the fixture no longer has a [[repo]] entry")
	}
	var decl strings.Builder
	env := append([]string(nil), r.repo.Env...)
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(repos)))
	for i, repo := range repos {
		decl.WriteString("[[repo]]\nurl = " + strconv.Quote(repo.URL) + "\nref = " + strconv.Quote(repo.Ref) + "\n\n")
		n := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"=url."+served[repo.URL]+".insteadOf", "GIT_CONFIG_VALUE_"+n+"="+repo.URL)
	}
	r.bundle["remote.toml"] = repoBlocks.ReplaceAllString(cur, "")
	r.bundle["remote.toml"] = strings.Replace(r.bundle["remote.toml"], "[network]\n", decl.String()+"[network]\n", 1)
	nodeFetchEnv = env
}

// dirNames is the sorted names in dir; none when dir is absent.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A node joins with exactly its definition's [[repo]] entries: each is cloned at
// its declared ref into its clone name, and nothing else is under the node's repos.
func TestNodeJoinClonesExactlyTheDeclaredRepos(t *testing.T) {
	r := newNodeRig(t)
	widgets := remotedef.Repo{URL: "https://github.com/acme/widgets.git", Ref: "main"}
	gadgets := remotedef.Repo{URL: "https://github.com/acme/gadgets.git", Ref: "v1"}
	r.setRepos([]remotedef.Repo{widgets, gadgets}, map[string]string{
		widgets.URL: r.repo.URL,
		gadgets.URL: r.repo.Also(t, "gadgets"),
	})
	run, _, joined, _ := r.open(t)

	reposDir := filepath.Join(r.nodeRoot, "repos")
	if got := dirNames(t, reposDir); !reflect.DeepEqual(got, []string{"gadgets", "widgets"}) {
		t.Fatalf("the node's repos = %v, want exactly [gadgets widgets]", got)
	}
	for name, want := range map[string]string{"widgets": r.repo.Head, "gadgets": r.repo.Tagged} {
		if head := gittest.Git(t, filepath.Join(reposDir, name), "rev-parse", "HEAD"); head != want {
			t.Errorf("%s HEAD = %s, want %s", name, head, want)
		}
	}
	rep := run.Report
	if rep.Error != "" || len(rep.Repos) != 2 ||
		rep.Repos[0] != (remotenode.FetchedRepo{Name: "widgets", URL: widgets.URL, Ref: "main", Commit: r.repo.Head}) ||
		rep.Repos[1] != (remotenode.FetchedRepo{Name: "gadgets", URL: gadgets.URL, Ref: "v1", Commit: r.repo.Tagged}) {
		t.Errorf("report = %+v", rep)
	}

	if err := run.End(); err != nil {
		t.Errorf("End: %v", err)
	}
	if err := waitJoin(t, joined); err != nil {
		t.Errorf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
}

// A definition with no [[repo]] clones nothing, and the run starts and works.
func TestNodeJoinWithNoReposStarts(t *testing.T) {
	r := newNodeRig(t)
	r.setRepos(nil, nil)
	run, _, joined, _ := r.open(t)

	if rep := run.Report; rep.Error != "" || len(rep.Repos) != 0 {
		t.Errorf("report = %+v, want a ready node with no repos", rep)
	}
	if got := dirNames(t, filepath.Join(r.nodeRoot, "repos")); len(got) != 0 {
		t.Errorf("the node cloned %v with no [[repo]] declared", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	body, err := run.Request(ctx, remotenode.AgentJob, remotenode.AgentRequest{Text: "no repos"})
	if err != nil {
		t.Fatalf("agent job: %v\nnode log:\n%s", err, r.log)
	}
	if reply, err := remotenode.DecodeAgentReply(body); err != nil || !strings.Contains(reply.Text, "no repos") {
		t.Errorf("agent reply = %+v, %v", reply, err)
	}

	if err := run.End(); err != nil {
		t.Errorf("End: %v", err)
	}
	if err := waitJoin(t, joined); err != nil {
		t.Errorf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
}

// revokeOnFirstSecretRead makes the satelle.dev fake revoke the node token the
// moment the node first asks it for a secret: after the definition fetch, as the
// join reads its secrets.
func revokeOnFirstSecretRead(fake *devtest.Node) {
	inner := fake.Client().Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	var once sync.Once
	nodeHTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/secrets/") {
			once.Do(fake.Revoke)
		}
		return inner.RoundTrip(req)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// AC8: a node whose token is revoked stops at join, as it reads its secrets, with a
// message saying so and naming the route; nothing further is asked of satelle.dev,
// the agent never runs, the relay is never dialled and at most one end call is
// made. The secrets are read once at join (sty_2dabc0f6), so join is where a
// revoked token is found.
func TestNodeJoinStopsWhenItsTokenIsRevoked(t *testing.T) {
	r := newNodeRig(t)
	fake := r.serve("nod_a", "node-token", r.encode())
	revokeOnFirstSecretRead(fake)

	err := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if err == nil || !strings.Contains(err.Error(), "node token was refused or revoked") ||
		!strings.Contains(err.Error(), "POST /api/v1/remote-nodes/") || !strings.Contains(err.Error(), "/secrets/CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("node join ended with %v, want the token refused naming the secrets route", err)
	}
	if _, serr := os.Stat(r.marker); serr == nil {
		t.Error("the agent ran after the token was refused")
	}
	if n := fake.Count(http.MethodGet, "/definition"); n != 1 {
		t.Errorf("definition fetches = %d, want only the one before the 401", n)
	}
	if n := fake.Count(http.MethodPost, "/secrets/CLAUDE_CODE_OAUTH_TOKEN"); n != 1 {
		t.Errorf("secret reads = %d, want only the refused one", n)
	}
	if n := fake.Count(http.MethodPost, "/end"); n > 1 {
		t.Errorf("end calls = %d, want at most 1", n)
	}
	if n := r.relay.Attempts(); n != 0 {
		t.Errorf("relay dials = %d, want none", n)
	}
}

// sty_2dabc0f6 AC4: a declared secret satelle.dev refuses (not declared or not
// granted) fails the node's start, naming the key, as a github-app refusal does:
// the failed start is reported on the run's status, no agent runs and the relay
// is never dialled.
func TestNodeJoinRefusedSecretFailsTheStartNamingTheKey(t *testing.T) {
	r := newNodeRig(t)
	r.fakeSetup = func(f *devtest.Node) { delete(f.Secrets, "CLAUDE_CODE_OAUTH_TOKEN") }
	fake := r.serve("nod_a", "node-token", r.encode())

	jerr := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if jerr == nil || !strings.Contains(jerr.Error(), "secret CLAUDE_CODE_OAUTH_TOKEN refused") {
		t.Fatalf("join ended with %v, want a refusal naming the key", jerr)
	}
	assertStartFailureStatuses(t, fake, 0)
	st := fake.Statuses()
	if len(st) == 0 || !strings.Contains(st[len(st)-1]["findings"], "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("the failed start's status = %v, want its findings naming the key", st)
	}
	if n := fake.Count(http.MethodPost, "/secrets/CLAUDE_CODE_OAUTH_TOKEN"); n != 1 {
		t.Errorf("secret reads = %d, want the one refused", n)
	}
	if _, serr := os.Stat(r.marker); serr == nil {
		t.Error("the agent started after a secret was refused")
	}
	if n := r.relay.Attempts(); n != 0 {
		t.Errorf("relay dials = %d, want none", n)
	}
}

// claudeTeamOverride is a team value satelle holds in its own store, as
// pr-approve declares its agent CLI credential.
const claudeTeamOverride = `"satelle:CLAUDE_CODE_OAUTH_TOKEN"`

// redactedKeys names, for a failure message, which keys of want got carries with
// the right value, never the values themselves.
func redactedKeys(got, want map[string]string) []string {
	var out []string
	for k, v := range want {
		state := "missing"
		switch {
		case got[k] == v:
			state = "present"
		case got[k] != "":
			state = "wrong value"
		}
		out = append(out, k+": "+state)
	}
	sort.Strings(out)
	return out
}

// AC7: a definition fetch that fails outright (the server is not reachable, or does
// not know the node) still ends the node once; AC10: the fake keys every route by
// the node id, so a node started with another --session than the fake's gets a 404.
func TestNodeJoinWithAnotherSessionThanTheServersIDIsNotFound(t *testing.T) {
	r := newNodeRig(t)
	fake := r.serve("nod_a", "node-token", r.encode())

	err := r.joinNow(r.relay.URL(), "nod_b", "node-token")
	if err == nil || !strings.Contains(err.Error(), "definition") {
		t.Fatalf("join = %v, want the definition fetch to fail", err)
	}
	calls := nonStatusCalls(fake)
	if len(calls) == 0 || calls[0].Path != "/api/v1/remote-nodes/nod_b/definition" || calls[0].Status != http.StatusNotFound {
		t.Errorf("calls = %+v, want a 404 for the definition fetch under nod_b", calls)
	}
	if fake.Count(http.MethodGet, "/definition") != 0 {
		t.Error("the fake served a definition under another id")
	}
}

// A bad token at satelle.dev stops the join at the definition fetch with the
// token's refusal, and makes no further calls beyond the one end.
func TestNodeJoinWithABadTokenIsRefused(t *testing.T) {
	r := newNodeRig(t)
	fake := r.serve("nod_a", "node-token", r.encode())

	err := r.joinNow(r.relay.URL(), "nod_a", "wrong-token")
	if err == nil || !strings.Contains(err.Error(), "node token was refused or revoked") || !strings.Contains(err.Error(), "/definition") {
		t.Fatalf("join = %v, want the token refused naming the definition route", err)
	}
	if calls := fake.Calls(); len(calls) != 2 || calls[1].Path != "/api/v1/remote-nodes/nod_a/end" {
		t.Errorf("calls = %+v, want the refused definition and one end", calls)
	}
}

// AC9: a relay that refuses the node token (401) is reported as the token's
// refusal, and the node still ends itself once.
func TestNodeJoinRelayRefusalIsTheTokensRefusal(t *testing.T) {
	r := newNodeRig(t)
	fake := r.serve("nod_a", "node-token", r.encode())
	r.relay.RejectRemoteBearers(1)

	err := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if err == nil || !strings.Contains(err.Error(), "node token was refused or revoked") || !strings.Contains(err.Error(), "relay") {
		t.Fatalf("join = %v, want the token refused by the relay", err)
	}
	if got := r.relay.RemoteBearers(); !reflect.DeepEqual(got, []string{"node-token"}) {
		t.Errorf("relay remote bearers = %v", got)
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
}

// AC1: handed the node token on the first line of stdin, with stdin left open,
// `node join --token-stdin` joins exactly as `--token` does: the definition is
// fetched and the relay is joined with that token as the bearer, and the node
// ends itself once. The token is in no argv.
func TestNodeJoinTokenStdin(t *testing.T) {
	r := newNodeRig(t)
	r.tokenStdin = true
	run, fake, joined, spec := r.open(t)

	calls := nonStatusCalls(fake)
	if len(calls) == 0 || calls[0].Path != "/api/v1/remote-nodes/"+spec.Session+"/definition" || calls[0].Auth != "Bearer "+spec.Token {
		t.Fatalf("first call = %+v, want the definition fetch with the token read from stdin", calls)
	}
	if got := r.relay.RemoteBearers(); !reflect.DeepEqual(got, []string{spec.Token}) {
		t.Errorf("relay remote bearers = %v, want [the node token]", got)
	}
	if rep := run.Report; rep.Error != "" || rep.DefinitionVersion != "v7" {
		t.Errorf("report = %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(r.nodeRoot, ".satelle", "skills", "remote-work.md")); err != nil {
		t.Errorf("the definition was not installed: %v", err)
	}

	if err := run.End(); err != nil {
		t.Errorf("End: %v", err)
	}
	if err := waitJoin(t, joined); err != nil {
		t.Errorf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	if strings.Contains(r.log.String(), spec.Token) {
		t.Error("the node token is in the node's log")
	}
}

// refusedJoin runs `node join` with args and stdin, and fails the test unless
// satelle.dev, the relay and the node root were all left untouched. It returns
// the command's error.
func (r *nodeRig) refusedJoin(stdin io.Reader, args ...string) error {
	r.t.Helper()
	fake := r.serve("nod_a", "node-token", r.encode())
	root := NewRootCmd()
	resetFlagState(root)
	root.SetOut(io.Discard)
	root.SetErr(r.log)
	root.SetIn(stdin)
	root.SetArgs(append([]string{"node", "join", "--root", r.nodeRoot, "--relay", r.relay.URL(), "--session", "nod_a"}, args...))
	err := executeRoot(root)
	if _, serr := os.Stat(r.nodeRoot); !errors.Is(serr, fs.ErrNotExist) {
		r.t.Errorf("the node root exists after a refused join (stat err %v)", serr)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		r.t.Errorf("a refused join called satelle.dev: %+v", calls)
	}
	if r.relay.Attempts() != 0 {
		r.t.Error("a refused join reached the relay")
	}
	return err
}

// AC2: an empty first line, or a stdin that fails, refuses with fixed text that
// holds no token, before the node root is created or initialised.
func TestNodeJoinTokenStdinRefusals(t *testing.T) {
	const sentinel = "sekret-token"
	for _, tc := range []struct {
		name  string
		stdin io.Reader
		want  string
	}{
		{"empty line", strings.NewReader("\n"), "join: --token-stdin: no token on stdin"},
		{"whitespace line", strings.NewReader("   \n"), "join: --token-stdin: no token on stdin"},
		{"nothing", strings.NewReader(""), "join: --token-stdin: no token on stdin"},
		{"read error", io.MultiReader(strings.NewReader(sentinel), iotest.ErrReader(errors.New("boom "+sentinel))), "join: --token-stdin: cannot read stdin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newNodeRig(t).refusedJoin(tc.stdin, "--token-stdin")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("join = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Error("the refusal contains the token")
			}
		})
	}
}

// AC3: exactly one of --token and --token-stdin is given, which flag validation
// checks before anything runs.
func TestNodeJoinTokenFlagsAreExclusiveAndRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"both flags", []string{"--token", "t", "--token-stdin"}, "none of the others can be"},
		{"neither flag", nil, "at least one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newNodeRig(t).refusedJoin(strings.NewReader("tok\n"), tc.args...)
			if err == nil || !strings.Contains(err.Error(), "[token token-stdin]") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("join = %v, want a flag-group refusal containing %q", err, tc.want)
			}
		})
	}
}
