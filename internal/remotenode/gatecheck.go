package remotenode

// The starter side of a check gate placed on a remote. A gate with placement
// remote:<id> runs its check skill on a node of that remote: GateChecker
// starts a docker-local node's container here, or has satelle-server start a
// fly-sprite node (gatesprite.go); setup brings up any service the check
// needs. It hands it a git bundle of the pushed head with the check
// script, its stdin and a wrapper, has it fetch and check out the head, runs
// the check there and brings its exit code and output back. Each run is its own
// node and container, so gates run at once on separate nodes; nothing is
// seated and no run file is kept: the gate.run the kernel records is the
// record. The node is given no secrets and fetches the code over no network.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

// The files a gate node reads from <dir>/in and writes to <dir>/out.
const (
	gateBundle  = "head.bundle"
	gateShallow = "shallow"
	gateScript  = "check"
	gatePayload = "payload.json"
	gateWrapper = "run-check"
	gateExit    = "exit"
	gateLog     = "log"
)

// The job types a gate node serves beside AgentJob: gate.hello returns the
// node's gate root and the digest of the definition it runs, gate.put and
// gate.get move files under that root, gate.exec runs argv in it.
const (
	GateHelloJob = "gate.hello"
	GatePutJob   = "gate.put"
	GateGetJob   = "gate.get"
	GateExecJob  = "gate.exec"
)

// The channel's frame bounds what one gate job carries: a file moves in
// chunks of gateChunkBytes, and gate.exec returns its output's last
// gateExecTail bytes.
const (
	gateChunkBytes = 512 << 10
	gateExecTail   = 256 << 10
)

// GateChecker runs check gates on a node of the remote a gate is placed on.
// A docker-local remote's node is a container started here; a fly-sprite
// remote's node is started by satelle-server through Sprites and reached over
// the sealed channel. Tree is the work tree the head is bundled from and
// DataDir its .satelle directory (where the remote definitions are); Out
// receives the progress lines.
type GateChecker struct {
	Tree, DataDir string
	Docker        provider.DockerLocal
	// Sprites starts a fly-sprite remote's node; nil when this repo has no
	// bound project.
	Sprites SpriteStarter
	Out     io.Writer
}

// SpriteStarter starts a node of a fly-sprite remote for one gate check.
type SpriteStarter interface {
	StartSprite(ctx context.Context, s SpriteStart) (SpriteNode, error)
}

// SpriteStart is one fly-sprite node to start: the remote, the item the gate
// checks, and the definition this repo holds for the remote (as LoadLocal
// reads it) with the sha256 of its remote.toml.
type SpriteStart struct {
	Remote, Item     string
	Definition       remotedef.Definition
	DefinitionSHA256 string
}

// SpriteNode is a started fly-sprite node: its id, the handle the check runs
// through and the absolute gate root on the node.
type SpriteNode struct {
	Node   string
	Handle provider.Handle
	Root   string
}

// RunRemote runs c on a new node of c.Remote, bounded as a whole by c.Timeout
// (the node's start and setup, the fetch, the check and collecting its result).
// A definition a gate node cannot be given ([secrets], [[repo]], a task
// drive) is refused before anything starts. On docker-local, an accepted
// run's container is removed (unless [lifecycle] scope = "job"); a reject or a
// failure leaves it to inspect and says how to remove it. On fly-sprite the
// node is always ended: its lifecycle is satelle-server's. A run cut at its
// bound is removed and returns a *gate.TimeoutError naming the phase it was
// cut in. A run that brings back no exit code is an error, never a verdict.
func (g GateChecker) RunRemote(ctx context.Context, c gate.RemoteCheck) (gate.RemoteOutcome, error) {
	out := g.Out
	if out == nil {
		out = io.Discard
	}
	def, bundle, err := LoadLocal(g.DataDir, c.Remote)
	if err != nil {
		return gate.RemoteOutcome{}, err
	}
	if err := gateNodeRefusal(def); err != nil {
		return gate.RemoteOutcome{}, err
	}
	digest, err := DefinitionDigest(bundle)
	if err != nil {
		return gate.RemoteOutcome{}, err
	}
	limit := c.Timeout
	if limit <= 0 {
		limit = gate.DefaultCheckTimeout
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	var (
		res   gate.RemoteOutcome
		h     provider.Handle
		phase = "starting the node"
		tmp   string
		root  string
		// leave: a reject or a failure leaves the machine to inspect
		// (docker-local); otherwise it is always ended.
		leave bool
	)
	// end stops the machine and says so.
	end := func(ctx context.Context) string {
		err := h.Stop(ctx)
		switch {
		case leave && err != nil:
			return fmt.Sprintf("removing the container %s failed: %v", h.ID(), err)
		case leave:
			return fmt.Sprintf("the container %s is removed", h.ID())
		case err != nil:
			return fmt.Sprintf("ending node %s failed: %v", h.ID(), err)
		}
		return fmt.Sprintf("node %s ended", h.ID())
	}
	// leftOrEnded leaves a docker-local container to inspect and says how to
	// remove it, or ends any other node and says so.
	leftOrEnded := func(ctx context.Context) string {
		if leave {
			return leftNote(h)
		}
		return end(ctx)
	}
	// fail ends a run that gave no result: a cut run's machine is removed
	// and the cut named; any other leaves a docker-local container to inspect.
	fail := func(err error, detail string) (gate.RemoteOutcome, error) {
		if ctx.Err() != nil {
			keep := context.WithoutCancel(ctx)
			var b strings.Builder
			fmt.Fprintf(&b, "cut while %s", phase)
			if res.Node != "" {
				fmt.Fprintf(&b, " on node %s", res.Node)
			}
			if h != nil && tmp != "" {
				if log := collectLog(keep, h, root, tmp); log != "" {
					fmt.Fprintf(&b, "\n%s", log)
				}
			}
			if h != nil {
				fmt.Fprintf(&b, "\n%s", end(keep))
			}
			res.Output = b.String()
			if parent.Err() != nil {
				return res, parent.Err()
			}
			return res, &gate.TimeoutError{Bound: limit}
		}
		res.Output = strings.TrimSpace(detail)
		if h != nil {
			res.Output = strings.TrimSpace(res.Output + "\n" + leftOrEnded(context.WithoutCancel(ctx)))
		}
		return res, err
	}

	switch def.Provider {
	case remotedef.ProviderDockerLocal:
		leave, root, phase = true, LocalNodeDir, "building the node image"
		if err := ensureNodeImage(ctx, g.Docker, g.Tree, def, out); err != nil {
			return fail(err, "")
		}
		node, err := newNodeID()
		if err != nil {
			return fail(err, "")
		}
		res.Node, phase = node, "starting the node"
		h, err = g.Docker.Start(ctx, def, provider.Run{NodeID: node})
		if h != nil {
			fmt.Fprintf(out, "gate %s: node %s in container %s\n", c.Gate, node, h.ID())
		}
		if err != nil {
			return fail(fmt.Errorf("node failed to start: %w", err), "")
		}
	case remotedef.ProviderFlySprite:
		if g.Sprites == nil {
			return res, fmt.Errorf("gate %s is placed on %s remote %s, which needs a bound project (satelle project bind)", c.Gate, def.Provider, c.Remote)
		}
		sn, err := g.Sprites.StartSprite(ctx, SpriteStart{Remote: c.Remote, Item: c.Env.Item, Definition: def, DefinitionSHA256: digest})
		res.Node = sn.Node
		if err != nil {
			return fail(fmt.Errorf("node failed to start: %w", err), "")
		}
		h, root = sn.Handle, sn.Root
		if h == nil || !path.IsAbs(root) {
			if h != nil {
				_ = h.Stop(context.WithoutCancel(ctx))
			}
			return res, fmt.Errorf("node %s started with no handle or no absolute gate root (%q)", sn.Node, root)
		}
		fmt.Fprintf(out, "gate %s: node %s of %s remote %s\n", c.Gate, sn.Node, def.Provider, c.Remote)
	default:
		return res, fmt.Errorf("gate %s is placed on remote %s, whose provider %s runs no gate check", c.Gate, c.Remote, def.Provider)
	}

	phase = "staging the head"
	tmp, err = os.MkdirTemp("", "satelle-gate-")
	if err != nil {
		return fail(err, "")
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, "in")
	shallow, err := stageGate(g.Tree, in, c)
	if err != nil {
		return fail(err, "")
	}
	if _, err := h.Exec(ctx, "mkdir", "-p", root); err != nil {
		return fail(err, "")
	}
	if err := h.CopyIn(ctx, in, path.Join(root, "in")); err != nil {
		return fail(err, "")
	}

	phase = "fetching the head"
	work := path.Join(root, "work")
	type fetchStep struct {
		name string
		argv []string
	}
	steps := []fetchStep{{"init", []string{"git", "init", "-q", work}}}
	// A shallow tree's bundle ends at its shallow commits: the node marks them
	// shallow too, or its fetch looks for their parents and fails.
	if shallow {
		steps = append(steps, fetchStep{"shallow", []string{"cp", path.Join(root, "in", gateShallow), path.Join(work, ".git", gateShallow)}})
	}
	steps = append(steps,
		fetchStep{"fetch", []string{"git", "-C", work, "fetch", "-q", path.Join(root, "in", gateBundle), c.Head.Ref}},
		fetchStep{"checkout", []string{"git", "-C", work, "checkout", "-q", "--detach", c.Head.Commit}},
	)
	for _, step := range steps {
		if b, err := h.Exec(ctx, step.argv...); err != nil {
			return fail(fmt.Errorf("the node could not check out the head %s (git %s): %w", c.Head.Commit, step.name, err), string(b))
		}
	}
	got, err := h.Exec(ctx, "git", "-C", work, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(got)) != c.Head.Commit {
		return fail(fmt.Errorf("the node could not check out the head %s: it is at %q (%v)", c.Head.Commit, strings.TrimSpace(string(got)), err), "")
	}

	phase = "running the check"
	fmt.Fprintf(out, "gate %s: running %s on node %s at %s\n", c.Gate, c.Head.Ref, res.Node, c.Head.Commit)
	execOut, execErr := h.Exec(ctx, "sh", path.Join(root, "in", gateWrapper), root)

	if ctx.Err() != nil {
		return fail(ctx.Err(), "")
	}
	phase = "collecting the result"
	exitFile := filepath.Join(tmp, "back", gateExit)
	if err := os.MkdirAll(filepath.Dir(exitFile), 0o755); err != nil {
		return fail(err, "")
	}
	var code int
	cerr := h.CopyOut(ctx, path.Join(root, "out", gateExit), exitFile)
	if cerr == nil {
		var b []byte
		if b, cerr = os.ReadFile(exitFile); cerr == nil {
			code, cerr = parseExit(b)
		}
	}
	if cerr != nil {
		detail := tailOf(string(execOut), 2000)
		if execErr != nil {
			detail = strings.TrimSpace(detail + "\n" + execErr.Error())
		}
		return fail(fmt.Errorf("the node returned no result: %w", cerr), detail)
	}
	res.Exit = code
	res.Output = collectLog(ctx, h, root, tmp)
	if code == 0 && leave && def.Lifecycle.Scope != remotedef.ScopeJob {
		if err := h.Stop(context.WithoutCancel(ctx)); err != nil {
			res.Output = strings.TrimSpace(res.Output + fmt.Sprintf("\nremoving the container %s failed: %v", h.ID(), err))
		}
		return res, nil
	}
	res.Output = strings.TrimSpace(res.Output + "\n" + leftOrEnded(context.WithoutCancel(ctx)))
	return res, nil
}

// leftNote says the node's container is left, and how to remove it.
func leftNote(h provider.Handle) string {
	return fmt.Sprintf("the container %s is left to inspect: docker exec -it %s sh; remove it with docker rm -f %s", h.ID(), h.ID(), h.ID())
}

// collectLog is the check's output the node wrote under root, "" when there
// is none.
func collectLog(ctx context.Context, h provider.Handle, root, tmp string) string {
	f := filepath.Join(tmp, "back", gateLog)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return ""
	}
	if err := h.CopyOut(ctx, path.Join(root, "out", gateLog), f); err != nil {
		return ""
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// stageGate writes the node's inputs into in: the bundle of the head's ref,
// the check script, its stdin and the wrapper that runs it; and, when the tree
// is a shallow clone, its shallow commits, where the bundle's history ends. It
// reports whether it wrote them.
func stageGate(tree, in string, c gate.RemoteCheck) (bool, error) {
	if err := os.MkdirAll(in, 0o755); err != nil {
		return false, err
	}
	repo := gitfacts.Repo{Dir: tree}
	if err := repo.Bundle(filepath.Join(in, gateBundle), c.Head.Ref); err != nil {
		return false, err
	}
	shallow, err := repo.ShallowCommits()
	if err != nil {
		return false, err
	}
	if len(shallow) > 0 {
		if err := os.WriteFile(filepath.Join(in, gateShallow), []byte(strings.Join(shallow, "\n")+"\n"), 0o644); err != nil {
			return false, err
		}
	}
	for name, b := range map[string]string{
		gateScript:  c.Script,
		gatePayload: string(c.Env.Payload()),
		gateWrapper: gateWrapperScript(c.Env, strings.HasPrefix(c.Script, "#!")),
	} {
		if err := os.WriteFile(filepath.Join(in, name), []byte(b), 0o644); err != nil {
			return false, err
		}
	}
	return len(shallow) > 0, nil
}

// gateWrapperScript is the sh script the node runs as `sh run-check <dir>`: it
// runs the check in <dir>/work as a local check runs (a script starting with
// "#!" as a file, any other through sh), with the transition in the
// environment and on stdin, its output to <dir>/out/log and its exit code to
// <dir>/out/exit. The root is an argument so no node path is written into it.
func gateWrapperScript(env gate.CheckEnv, shebang bool) string {
	var b strings.Builder
	b.WriteString("# Runs a gate's check on this node; written by satelle.\nset -u\nroot=$1\nmkdir -p \"$root/out\" || exit 1\n")
	for _, kv := range env.Vars() {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "export %s=%s\n", k, shQuote(v))
	}
	b.WriteString("cd \"$root/work\" || exit 1\n")
	redirect := ` < "$root/in/` + gatePayload + `" > "$root/out/` + gateLog + `" 2>&1`
	if shebang {
		b.WriteString(`chmod 700 "$root/in/` + gateScript + `" || exit 1` + "\n")
		b.WriteString(`"$root/in/` + gateScript + `"` + redirect + "\n")
	} else {
		b.WriteString(`sh "$root/in/` + gateScript + `"` + redirect + "\n")
	}
	b.WriteString(`echo $? > "$root/out/` + gateExit + `"` + "\n")
	return b.String()
}

// parseExit reads the exit file the wrapper wrote: one integer.
func parseExit(b []byte) (int, error) {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, errors.New("the exit file is empty")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("the exit file holds %q, not an exit code", s)
	}
	return n, nil
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
