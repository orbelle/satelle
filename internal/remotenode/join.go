package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

// JoinOptions say which node this is and where it works.
type JoinOptions struct {
	Server, NodeID, Token string
	// Dir is the node's working root: repos/, work/ and runtime/ go under it.
	Dir string
	// NoSetup skips [setup]: the provider already ran it in the machine.
	NoSetup bool
	// Log is the node's own log: setup output and progress. Nil discards.
	Log io.Writer
	// Version is the satelle the node runs, for its report.
	Version string
	// Heartbeat is the interval of ReportStatus while the node works; zero is
	// 15 seconds.
	Heartbeat time.Duration
	// SetupTimeout bounds each setup command; zero is 20 minutes.
	SetupTimeout time.Duration
	// GitEnv is added to git's environment when the node fetches its repos.
	GitEnv []string
	// Dispatcher runs the node's bindings; nil is agent.Runner.
	Dispatcher agent.Dispatcher
	// ChanOpts configure a performer node's channel.
	ChanOpts []remotechan.Option
}

// Outcome is how a node's run ended: the verdict it concluded with and why.
type Outcome struct {
	Verdict string
	Notes   string
}

// setupLogTail bounds the setup output a report carries.
const setupLogTail = 8 << 10

// Join runs one node end to end. It reads its definition, run and secrets
// from satelle-server with the node token, fetches the declared repos
// read-only, runs the definition's setup (and the provider's), and then its
// run:
//
//   - item: pull the item's chain, drive it along its workflow on the kernel,
//     push its chain back;
//   - task: run the task once (kernel.TaskRun), drive the execution it
//     created, push;
//   - performer: publish the channel key, join the relay and perform the steps
//     the starter sends until it closes the channel.
//
// While it works it heartbeats through ReportStatus with its progress and the
// tail of its setup output, and it concludes with a verdict (accept or
// reject) and notes. It always ends the node (EndNode), which revokes the
// token. A secret's value goes only to the setup commands' and agents'
// environment: never to disk, a report, the ledger or an error.
func Join(ctx context.Context, o JoinOptions) (Outcome, error) {
	if o.NodeID == "" || o.Token == "" || o.Server == "" {
		return Outcome{}, errors.New("node join needs the server, the node id and the node token")
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = 15 * time.Second
	}
	if o.SetupTimeout <= 0 {
		o.SetupTimeout = 20 * time.Minute
	}
	if o.Dispatcher == nil {
		o.Dispatcher = agent.Runner{}
	}
	nc, err := NewNodeClient(o.Server, o.NodeID, o.Token)
	if err != nil {
		return Outcome{}, err
	}
	defer nc.Close()
	j := &joiner{o: o, nc: nc}
	out, runErr := j.work(ctx)
	if runErr != nil {
		out = Outcome{Verdict: kernel.VerdictReject, Notes: j.redacted(runErr.Error())}
	}
	cctx := context.WithoutCancel(ctx)
	var refused *TokenRefusedError
	if !errors.As(runErr, &refused) {
		if _, err := nc.Status(cctx, NodeStatus{State: StateConcluded, RunState: StateConcluded, ItemID: j.itemID(),
			Verdict: out.Verdict, Notes: out.Notes, Progress: j.lastProgress(), SetupLog: j.setupTail()}); err != nil && runErr == nil {
			runErr = err
		}
	}
	if err := nc.End(cctx); err != nil && runErr == nil {
		runErr = err
	}
	return out, runErr
}

type joiner struct {
	o   JoinOptions
	nc  *NodeClient
	run RunDescriptor
	// digest is the sha256 of the remote.toml the node was served (gate.hello).
	digest string

	mu           sync.Mutex
	progress     string
	setup        tailBuffer
	secretValues []string // values redacted from every report
}

func (j *joiner) itemID() string { return j.run.ItemID }

func (j *joiner) redacted(s string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return redact(s, j.secretValues)
}

func (j *joiner) lastProgress() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.progress
}

// setupTail is the bounded tail of setup output, with every secret value
// redacted: a command that echoes one must not carry it into a report.
func (j *joiner) setupTail() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return redact(j.setup.String(), j.secretValues)
}

func redact(s string, values []string) string {
	for _, v := range values {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[secret]")
		}
	}
	return s
}

// say records a progress line, logs it, and reports it at the next heartbeat.
func (j *joiner) say(format string, args ...any) {
	j.mu.Lock()
	line := redact(fmt.Sprintf(format, args...), j.secretValues)
	j.progress = line
	j.mu.Unlock()
	fmt.Fprintln(j.o.Log, "node:", line)
}

func (j *joiner) heartbeat(ctx context.Context, stop context.CancelCauseFunc, step func() string) {
	t := time.NewTicker(j.o.Heartbeat)
	defer t.Stop()
	for {
		reply, err := j.nc.Status(ctx, NodeStatus{State: StateRunning, RunState: StateRunning, ItemID: j.itemID(),
			Step: step(), Progress: j.lastProgress(), SetupLog: j.setupTail()})
		var refused *TokenRefusedError
		switch {
		case errors.As(err, &refused):
			stop(err)
			return
		case err == nil && reply.Stop:
			stop(errors.New("satelle-server asked the node to stop"))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (j *joiner) work(parent context.Context) (Outcome, error) {
	o := j.o
	bundle, version, err := j.nc.Definition(parent)
	if err != nil {
		return Outcome{}, err
	}
	def, overlay, err := ReadBundle(bundle)
	if err != nil {
		return Outcome{}, err
	}
	if j.digest, err = DefinitionDigest(bundle); err != nil {
		return Outcome{}, err
	}
	if j.run, err = j.nc.Run(parent); err != nil {
		return Outcome{}, err
	}
	prov, err := provider.For(def.Provider)
	if err != nil {
		return Outcome{}, err
	}
	j.say("definition %s %s, run %s %s", def.Name, version, j.run.Mode, j.run.ItemID)

	ctx, stop := context.WithCancelCause(parent)
	defer stop(nil)
	node := NewNode()
	step := func() string {
		if j.run.Mode == ModePerformer {
			return node.Status().Step
		}
		return StepWorking
	}
	hbDone := make(chan struct{})
	go func() { defer close(hbDone); j.heartbeat(ctx, stop, step) }()
	defer func() { stop(nil); <-hbDone }()
	release := prov.Hold(ctx, "satelle-"+o.NodeID, o.Log)
	defer release()

	env, err := j.secrets(ctx, def)
	if err != nil {
		return Outcome{}, err
	}
	reposDir := filepath.Join(o.Dir, "repos")
	fetched, err := FetchRepos(ctx, reposDir, def, def.Name, def.Repos, o.GitEnv)
	if err != nil {
		return Outcome{}, err
	}
	j.say("fetched %d repos read-only", len(fetched))
	tree, root := j.tree(reposDir, fetched)
	if err := os.MkdirAll(tree, 0o755); err != nil {
		return Outcome{}, err
	}
	disableSwap, err := EnableSwap(ctx, def.Machine.Swap)
	if err != nil {
		return Outcome{}, err
	}
	defer func() { _ = disableSwap() }()
	if !o.NoSetup {
		if err := j.runSetup(ctx, tree, def, prov, env); err != nil {
			return Outcome{}, err
		}
	}
	k, err := openNodeKernel(root, tree, filepath.Join(o.Dir, "runtime"), overlay, o.Dispatcher, env)
	if err != nil {
		return Outcome{}, err
	}
	defer k.Close()
	actor := "node:" + o.NodeID
	report := Report{Version: o.Version, DefinitionVersion: version, Allow: def.Allow(), Repos: fetched,
		Bindings: k.Agents.Names(), Now: time.Now().UnixNano()}

	var out Outcome
	switch j.run.Mode {
	case ModeItem, ModeTask:
		out, err = j.drive(ctx, k, actor)
	case ModePerformer:
		out, err = j.perform(ctx, k, node, tree, report)
	default:
		return Outcome{Verdict: kernel.VerdictAccept, Notes: "set up; no run"}, nil
	}
	if cause := context.Cause(ctx); err == nil && cause != nil && ctx.Err() != nil && parent.Err() == nil {
		err = cause
	}
	return out, err
}

// secrets reads every key the definition declares; the values become KEY=value
// environment entries for setup and the agents, and nothing else.
func (j *joiner) secrets(ctx context.Context, def remotedef.Definition) ([]string, error) {
	var env []string
	for _, e := range def.SecretEntries() {
		v, err := j.nc.Secret(ctx, e.Key)
		if err != nil {
			return nil, err
		}
		env = append(env, e.Key+"="+v)
		j.mu.Lock()
		j.secretValues = append(j.secretValues, v)
		j.mu.Unlock()
	}
	if len(env) > 0 {
		j.say("read %d secrets", len(env))
	}
	return env, nil
}

// tree picks the working tree the kernel runs in: the first fetched repo that
// carries a .satelle directory, else <dir>/work.
func (j *joiner) tree(reposDir string, fetched []FetchedRepo) (tree, root string) {
	for _, r := range fetched {
		t := filepath.Join(reposDir, r.Name)
		if st, err := os.Stat(filepath.Join(t, ".satelle")); err == nil && st.IsDir() {
			return t, filepath.Join(t, ".satelle")
		}
	}
	t := filepath.Join(j.o.Dir, "work")
	return t, filepath.Join(t, ".satelle")
}

func (j *joiner) runSetup(ctx context.Context, dir string, def remotedef.Definition, prov provider.Provider, env []string) error {
	w := io.MultiWriter(j.o.Log, &lockedWriter{mu: &j.mu, w: &j.setup})
	for _, phase := range []struct {
		name string
		cmds []string
	}{{SetupOnCreate, def.Setup.OnCreate}, {SetupOnCreate, prov.SetupCommands()}, {SetupPostStart, def.Setup.PostStart}} {
		if len(phase.cmds) == 0 {
			continue
		}
		j.say("setup %s: %d commands", phase.name, len(phase.cmds))
		if err := RunSetup(ctx, dir, phase.name, phase.cmds, env, j.o.SetupTimeout, w); err != nil {
			return err
		}
	}
	return nil
}

// openNodeKernel installs the bundle's overlay into root, starts a
// configuration when the tree has none, and opens the kernel with the node's
// own runtime. The node performs every step itself: its bindings are placed
// locally, dispatched by dispatcher with env added.
func openNodeKernel(root, tree, runtime string, overlay []fileEntry, dispatcher agent.Dispatcher, env []string) (*kernel.Kernel, error) {
	if err := writeOverlay(root, overlay); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, "satelle.toml")); err != nil {
		if err := kernel.Init(root); err != nil {
			return nil, err
		}
	}
	k, err := kernel.Open(kernel.Options{Root: root, Runtime: runtime, Dir: tree})
	if err != nil {
		return nil, err
	}
	for name, b := range k.Agents {
		b.Placement = agent.PlaceLocal
		k.Agents[name] = b
	}
	k.Dispatcher = withEnv{inner: dispatcher, env: env}
	return k, nil
}

// withEnv hands the run's secrets to every binding the node dispatches.
type withEnv struct {
	inner agent.Dispatcher
	env   []string
}

func (w withEnv) Dispatch(ctx context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	req.Env = append(append([]string{}, req.Env...), w.env...)
	return w.inner.Dispatch(ctx, role, req)
}

// drive runs an item or task node: pull the item, drive it (or the execution
// a task run creates), push the chains back.
func (j *joiner) drive(ctx context.Context, k *kernel.Kernel, actor string) (Outcome, error) {
	if j.run.ItemID == "" || j.run.Project == "" {
		return Outcome{}, fmt.Errorf("the %s run names no item or project", j.run.Mode)
	}
	repl := j.replicator(k)
	if err := pullRun(ctx, repl, j.run.ItemID); err != nil {
		return Outcome{}, err
	}
	target := j.run.ItemID
	if j.run.Mode == ModeTask {
		exec, rec, err := k.TaskRun(j.run.ItemID, "", actor)
		if err != nil {
			return Outcome{}, err
		}
		j.say("task %s: execution %s over %d items", j.run.ItemID, exec.ID, len(rec.Inputs))
		target = exec.ID
	}
	j.say("driving %s", target)
	res, derr := k.Drive(ctx, target, actor)
	pushed, perr := repl.Push(ctx)
	if derr != nil {
		return Outcome{}, derr
	}
	if perr != nil {
		return Outcome{}, fmt.Errorf("push the ledger: %w", perr)
	}
	if pushed.Failed() {
		return Outcome{}, fmt.Errorf("push the ledger: %d item(s) diverged from the server", len(pushed.Diverged)+len(pushed.Refused))
	}
	j.say("%s at %s: %s", target, res.Item.Status, res.Notes)
	return Outcome{Verdict: res.Verdict, Notes: j.redacted(fmt.Sprintf("%s: %s", target, res.Notes))}, nil
}

// replicator moves the node's chains through satelle-server for its run's
// project, by the rules every store uses.
func (j *joiner) replicator(k *kernel.Kernel) *ledgerrepl.Replicator {
	store := &ledgerrepl.Dev{Client: j.nc.Hosted(), Project: j.run.Project, Server: j.o.Server, Ledger: k.Ledger, Cache: k.Cache}
	return &ledgerrepl.Replicator{Ledger: k.Ledger, Store: store, Refs: kernel.BlobRefs, Out: j.o.Log, Cache: k.Cache, Choose: k}
}

// pullRun pulls the run's item; a diverged or refused chain is an error.
func pullRun(ctx context.Context, repl *ledgerrepl.Replicator, itemID string) error {
	res, err := repl.Pull(ctx, itemID)
	if err != nil {
		return err
	}
	if res.Failed() {
		return fmt.Errorf("pull %s: %d diverged, %d refused: %s", itemID, len(res.Diverged), len(res.Refused), strings.Join(append(res.DivergedItems(), res.Refused...), "; "))
	}
	return nil
}

// perform serves a performer node: pull the run's item, publish the channel
// key, join the relay, and run each step the starter sends with the node's
// own binding, its prompt followed by the item's record built from the
// node's own ledger.
func (j *joiner) perform(ctx context.Context, k *kernel.Kernel, node *Node, tree string, report Report) (Outcome, error) {
	if j.run.ItemID != "" && j.run.Project != "" {
		if err := pullRun(ctx, j.replicator(k), j.run.ItemID); err != nil {
			return Outcome{}, err
		}
	}
	id, err := remotechan.NewIdentity()
	if err != nil {
		return Outcome{}, err
	}
	if err := j.nc.PublishChannelKey(ctx, id.Public()); err != nil {
		return Outcome{}, err
	}
	sess, err := remotechan.Join(ctx, j.nc.Hosted().Relay, j.o.NodeID, id, j.o.ChanOpts...)
	if err != nil {
		return Outcome{}, err
	}
	defer sess.Close()
	steps := 0
	node.Handle(AgentJob, func(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
		var req AgentRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		name := req.Binding
		if name == "" {
			name = j.run.Binding
		}
		b, ok := k.Agents[name]
		if !ok {
			return nil, fmt.Errorf("binding %q is not in the node's agents", name)
		}
		var rec kernel.Record
		if j.run.ItemID != "" {
			if rec, err = k.OpenRecord(j.run.ItemID); err != nil {
				return nil, err
			}
			defer rec.Close()
		}
		j.say("performing with %s", name)
		res, err := k.Dispatcher.Dispatch(ctx, agent.Perform, agent.Request{Binding: b, System: req.SystemPrompt, Prompt: req.Text + rec.Section, Dir: tree})
		if err != nil {
			return nil, err
		}
		steps++
		return json.Marshal(AgentReply{Text: res.Text, Model: res.Model, TokensIn: res.TokensIn, TokensOut: res.TokensOut})
	})
	// A gate check on this node (a fly-sprite remote) runs through the gate
	// jobs, beside the agent.
	if err := serveGateJobs(node, j.o.Dir, j.digest); err != nil {
		return Outcome{}, err
	}
	j.say("serving the starter")
	if err := node.Serve(ctx, sess, report); err != nil {
		return Outcome{}, err
	}
	return Outcome{Verdict: kernel.VerdictAccept, Notes: fmt.Sprintf("performed %d steps", steps)}, nil
}

// tailBuffer keeps the last setupLogTail bytes written.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > setupLogTail {
		t.b = t.b[len(t.b)-setupLogTail:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.ToValidUTF8(string(t.b), "") }

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
