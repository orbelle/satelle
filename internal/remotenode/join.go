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
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

type JoinOptions struct {
	Server, NodeID, Token string
	Dir                   string
	NoSetup               bool
	Log                   io.Writer
	Version               string
	Heartbeat             time.Duration
	SetupTimeout          time.Duration
	GitEnv                []string
	Dispatcher            agent.Dispatcher
	ChanOpts              []remotechan.Option
}

type Outcome struct {
	Verdict string
	Notes   string
}

const setupLogTail = 8 << 10

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
	o      JoinOptions
	nc     *NodeClient
	run    RunDescriptor
	digest string
	nr     *NodeRun

	mu           sync.Mutex
	progress     string
	setup        tailBuffer
	secretValues []string
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

func (j *joiner) say(format string, args ...any) {
	j.mu.Lock()
	line := redact(fmt.Sprintf(format, args...), j.secretValues)
	j.progress = line
	j.mu.Unlock()
	fmt.Fprintln(j.o.Log, "node:", line)
}

const heartbeatCallLimit = 30 * time.Second

func (j *joiner) heartbeat(ctx context.Context, stop context.CancelCauseFunc, step func() string) {
	t := time.NewTicker(j.o.Heartbeat)
	defer t.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), heartbeatCallLimit)
		reply, err := j.nc.Status(callCtx, NodeStatus{State: StateRunning, RunState: StateRunning, ItemID: j.itemID(),
			Step: step(), Progress: j.lastProgress(), SetupLog: j.setupTail()})
		cancel()
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
	tree, root, commit := j.tree(reposDir, fetched)
	if err := os.MkdirAll(tree, 0o755); err != nil {
		return Outcome{}, err
	}
	disableSwap, err := EnableSwap(ctx, def.Machine.Swap)
	if err != nil {
		return Outcome{}, err
	}
	defer func() { _ = disableSwap() }()
	if !o.NoSetup {
		if err := writeOverlay(root, overlay); err != nil {
			return Outcome{}, err
		}
		if err := j.runSetup(ctx, tree, root, def, prov, env); err != nil {
			return Outcome{}, err
		}
	}
	k, err := openNodeKernel(root, tree, filepath.Join(o.Dir, "runtime"), overlay, o.Dispatcher, env, nil)
	if err != nil {
		return Outcome{}, err
	}
	defer k.Close()
	var withhold []string
	for _, e := range def.SecretEntries() {
		withhold = append(withhold, e.Key)
	}
	nr, err := MarkNodeRun(o.Dir, o.NodeID, root, tree, withhold)
	if err != nil {
		return Outcome{}, err
	}
	defer nr.Restore()
	j.nr = nr
	k.Staged = &gate.Staged{Digest: nr.Digest(), Head: commit}
	actor := "node:" + o.NodeID
	report := Report{Version: o.Version, DefinitionVersion: version, Allow: def.Allow(), Repos: fetched,
		Bindings: k.Agents.Names(), Now: time.Now().UnixNano()}

	var out Outcome
	switch j.run.Mode {
	case ModeItem, ModeTask:
		out, err = j.drive(ctx, k, actor)
	case ModePerformer:
		out, err = j.perform(ctx, k, node, tree, report)
		if err == nil {
			err = nr.Verify()
		}
	default:
		return Outcome{Verdict: kernel.VerdictAccept, Notes: "set up; no run"}, nil
	}
	if cause := context.Cause(ctx); err == nil && cause != nil && ctx.Err() != nil && parent.Err() == nil {
		err = cause
	}
	return out, err
}

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

func (j *joiner) tree(reposDir string, fetched []FetchedRepo) (tree, root, commit string) {
	for _, r := range fetched {
		t := filepath.Join(reposDir, r.Name)
		if st, err := os.Stat(filepath.Join(t, ".satelle")); err == nil && st.IsDir() {
			return t, filepath.Join(t, ".satelle"), r.Commit
		}
	}
	t := filepath.Join(j.o.Dir, "work")
	return t, filepath.Join(t, ".satelle"), ""
}

func (j *joiner) runSetup(ctx context.Context, dir, root string, def remotedef.Definition, prov provider.Provider, env []string) error {
	w := io.MultiWriter(j.o.Log, &lockedWriter{mu: &j.mu, w: &j.setup})
	env = append(append([]string{}, env...), kernel.SubstrateVar(root))
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

func openNodeKernel(root, tree, runtime string, overlay []fileEntry, dispatcher agent.Dispatcher, env []string, redact func(string) string) (*kernel.Kernel, error) {
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
	machine, _ := config.MachineID(config.GlobalDir())
	if err := k.SetOrigin(machine, ledger.RoleNode, hook.Caller()); err != nil {
		k.Close()
		return nil, err
	}
	cats := []agent.Catalog{k.Agents}
	for _, cat := range k.PartAgents {
		cats = append(cats, cat)
	}
	for _, cat := range cats {
		for name, b := range cat {
			b.Placement = agent.PlaceLocal
			cat[name] = b
		}
	}
	k.Redact = redact
	k.Dispatcher = withEnv{inner: dispatcher, env: env, redact: redact}
	return k, nil
}

type withEnv struct {
	inner  agent.Dispatcher
	env    []string
	redact func(string) string
}

func (w withEnv) Dispatch(ctx context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	req.Env = append(append([]string{}, req.Env...), w.env...)
	if w.redact != nil {
		req.Redact = w.redact
	}
	return w.inner.Dispatch(ctx, role, req)
}

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
	if verr := j.nr.Verify(); verr != nil {
		return Outcome{}, verr
	}
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

func (j *joiner) replicator(k *kernel.Kernel) *ledgerrepl.Replicator {
	store := &ledgerrepl.Dev{Client: j.nc.Hosted(), Project: j.run.Project, Server: j.o.Server, Ledger: k.Ledger, Cache: k.Cache}
	return &ledgerrepl.Replicator{Ledger: k.Ledger, Store: store, Refs: kernel.BlobRefs, Out: j.o.Log, Cache: k.Cache, Choose: k, Mirrors: k}
}

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
		b, err := nodeBinding(k, j.run.ItemID, name)
		if err != nil {
			return nil, err
		}
		var rec kernel.Record
		if j.run.ItemID != "" {
			if rec, err = k.OpenRecord(j.run.ItemID); err != nil {
				return nil, err
			}
			defer rec.Close()
		}
		j.say("performing with %s", name)
		res, err := k.Dispatcher.Dispatch(ctx, agent.Perform, agent.Request{Binding: b, System: req.SystemPrompt, Prompt: req.Text + rec.Section, Dir: tree, Env: []string{k.SubstrateEnv()}})
		if err != nil {
			return nil, err
		}
		steps++
		return json.Marshal(AgentReply{Text: res.Text, Model: res.Model, Command: res.Command, TokensIn: res.TokensIn, TokensOut: res.TokensOut})
	})
	if err := serveGateJobs(node, j.o.Dir, j.digest); err != nil {
		return Outcome{}, err
	}
	j.say("serving the starter")
	if err := node.Serve(ctx, sess, report); err != nil {
		return Outcome{}, err
	}
	return Outcome{Verdict: kernel.VerdictAccept, Notes: fmt.Sprintf("performed %d steps", steps)}, nil
}

func nodeBinding(k *kernel.Kernel, itemID, name string) (agent.Binding, error) {
	cat := k.Agents
	if itemID != "" {
		var err error
		if cat, err = k.ItemAgents(itemID); err != nil {
			return agent.Binding{}, err
		}
	}
	b, ok := cat[name]
	if !ok {
		return agent.Binding{}, fmt.Errorf("binding %q is not in the node's agents", name)
	}
	return b, nil
}

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
