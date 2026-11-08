package remotenode

// The starter side of a check gate placed on a docker-local remote. A gate
// with placement remote:<id> runs its check skill on a node of that remote,
// here: GateChecker starts the node's container (setup brings up any service
// the check needs), hands it a git bundle of the pushed head with the check
// script, its stdin and a wrapper, has it fetch and check out the head, runs
// the check there and brings its exit code and output back. Each run is its own
// node and container, so gates run at once on separate nodes; nothing is
// seated and no run file is kept: the gate.run the kernel records is the
// record. The node is given no secrets and fetches over no network.

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
	gateScript  = "check"
	gatePayload = "payload.json"
	gateWrapper = "run-check"
	gateExit    = "exit"
	gateLog     = "log"
)

// GateChecker runs check gates on docker-local nodes started here. Tree is the
// work tree the head is bundled from and DataDir its .satelle directory (where
// the remote definitions are); Out receives the progress lines.
type GateChecker struct {
	Tree, DataDir string
	Docker        provider.DockerLocal
	Out           io.Writer
}

// RunRemote runs c on a new node of c.Remote, bounded as a whole by c.Timeout
// (the node's start and setup, the fetch, the check and collecting its result).
// A definition a local node cannot be given ([secrets], [[repo]], a task
// drive) is refused before anything starts. An accepted run's container is
// removed (unless [lifecycle] scope = "job"); a reject or a failure leaves it
// to inspect and says how to remove it; a run cut at its bound is removed and
// returns a *gate.TimeoutError naming the phase it was cut in. A run that
// brings back no exit code is an error, never a verdict.
func (g GateChecker) RunRemote(ctx context.Context, c gate.RemoteCheck) (gate.RemoteOutcome, error) {
	out := g.Out
	if out == nil {
		out = io.Discard
	}
	def, _, err := LoadLocal(g.DataDir, c.Remote)
	if err != nil {
		return gate.RemoteOutcome{}, err
	}
	if err := localRefusal(def); err != nil {
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
		phase = "building the node image"
		tmp   string
	)
	// fail ends a run that gave no result: a cut run's container is removed
	// and the cut named; any other leaves the container to inspect.
	fail := func(err error, detail string) (gate.RemoteOutcome, error) {
		if ctx.Err() != nil {
			keep := context.WithoutCancel(ctx)
			var b strings.Builder
			fmt.Fprintf(&b, "cut while %s", phase)
			if res.Node != "" {
				fmt.Fprintf(&b, " on node %s", res.Node)
			}
			if h != nil && tmp != "" {
				if log := collectLog(keep, h, tmp); log != "" {
					fmt.Fprintf(&b, "\n%s", log)
				}
			}
			if h != nil {
				if serr := h.Stop(keep); serr != nil {
					fmt.Fprintf(&b, "\nremoving the container %s failed: %v", h.ID(), serr)
				} else {
					fmt.Fprintf(&b, "\nthe container %s is removed", h.ID())
				}
			}
			res.Output = b.String()
			if parent.Err() != nil {
				return res, parent.Err()
			}
			return res, &gate.TimeoutError{Bound: limit}
		}
		res.Output = strings.TrimSpace(detail)
		if h != nil {
			res.Output = strings.TrimSpace(res.Output + "\n" + leftNote(h))
		}
		return res, err
	}

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

	phase = "staging the head"
	tmp, err = os.MkdirTemp("", "satelle-gate-")
	if err != nil {
		return fail(err, "")
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, "in")
	if err := stageGate(g.Tree, in, c); err != nil {
		return fail(err, "")
	}
	if _, err := h.Exec(ctx, "mkdir", "-p", LocalNodeDir); err != nil {
		return fail(err, "")
	}
	if err := h.CopyIn(ctx, in, path.Join(LocalNodeDir, "in")); err != nil {
		return fail(err, "")
	}

	phase = "fetching the head"
	work := path.Join(LocalNodeDir, "work")
	for _, step := range []struct {
		name string
		argv []string
	}{
		{"init", []string{"git", "init", "-q", work}},
		{"fetch", []string{"git", "-C", work, "fetch", "-q", path.Join(LocalNodeDir, "in", gateBundle), c.Head.Ref}},
		{"checkout", []string{"git", "-C", work, "checkout", "-q", "--detach", c.Head.Commit}},
	} {
		if b, err := h.Exec(ctx, step.argv...); err != nil {
			return fail(fmt.Errorf("the node could not check out the head %s (git %s): %w", c.Head.Commit, step.name, err), string(b))
		}
	}
	got, err := h.Exec(ctx, "git", "-C", work, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(got)) != c.Head.Commit {
		return fail(fmt.Errorf("the node could not check out the head %s: it is at %q (%v)", c.Head.Commit, strings.TrimSpace(string(got)), err), "")
	}

	phase = "running the check"
	fmt.Fprintf(out, "gate %s: running %s on node %s at %s\n", c.Gate, c.Head.Ref, node, c.Head.Commit)
	execOut, execErr := h.Exec(ctx, "sh", path.Join(LocalNodeDir, "in", gateWrapper), LocalNodeDir)

	if ctx.Err() != nil {
		return fail(ctx.Err(), "")
	}
	phase = "collecting the result"
	exitFile := filepath.Join(tmp, "back", gateExit)
	if err := os.MkdirAll(filepath.Dir(exitFile), 0o755); err != nil {
		return fail(err, "")
	}
	var code int
	cerr := h.CopyOut(ctx, path.Join(LocalNodeDir, "out", gateExit), exitFile)
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
	res.Output = collectLog(ctx, h, tmp)
	if code == 0 && def.Lifecycle.Scope != remotedef.ScopeJob {
		if err := h.Stop(context.WithoutCancel(ctx)); err != nil {
			res.Output = strings.TrimSpace(res.Output + fmt.Sprintf("\nremoving the container %s failed: %v", h.ID(), err))
		}
		return res, nil
	}
	res.Output = strings.TrimSpace(res.Output + "\n" + leftNote(h))
	return res, nil
}

// leftNote says the node's container is left, and how to remove it.
func leftNote(h provider.Handle) string {
	return fmt.Sprintf("the container %s is left to inspect: docker exec -it %s sh; remove it with docker rm -f %s", h.ID(), h.ID(), h.ID())
}

// collectLog is the check's output the node wrote, "" when there is none.
func collectLog(ctx context.Context, h provider.Handle, tmp string) string {
	f := filepath.Join(tmp, "back", gateLog)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return ""
	}
	if err := h.CopyOut(ctx, path.Join(LocalNodeDir, "out", gateLog), f); err != nil {
		return ""
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// stageGate writes the node's inputs into in: the bundle of the head's ref,
// the check script, its stdin and the wrapper that runs it.
func stageGate(tree, in string, c gate.RemoteCheck) error {
	if err := os.MkdirAll(in, 0o755); err != nil {
		return err
	}
	if err := (gitfacts.Repo{Dir: tree}).Bundle(filepath.Join(in, gateBundle), c.Head.Ref); err != nil {
		return err
	}
	for name, b := range map[string]string{
		gateScript:  c.Script,
		gatePayload: string(c.Env.Payload()),
		gateWrapper: gateWrapperScript(c.Env, strings.HasPrefix(c.Script, "#!")),
	} {
		if err := os.WriteFile(filepath.Join(in, name), []byte(b), 0o644); err != nil {
			return err
		}
	}
	return nil
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
