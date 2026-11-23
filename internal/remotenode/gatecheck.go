package remotenode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

const (
	gateBundle  = "head.bundle"
	gateShallow = "shallow"
	gateScript  = "check"
	gatePayload = "payload.json"
	gateWrapper = "run-check"
	gateExit    = "exit"
	gateLog     = "log"
)

const (
	GateHelloJob = "gate.hello"
	GatePutJob   = "gate.put"
	GateGetJob   = "gate.get"
	GateExecJob  = "gate.exec"
)

const (
	gateChunkBytes = 512 << 10
	gateExecTail   = 256 << 10
)

type GateChecker struct {
	Tree, DataDir string
	Docker        provider.DockerLocal
	Sprites       SpriteStarter
	Out           io.Writer
}

type SpriteStarter interface {
	StartSprite(ctx context.Context, s SpriteStart) (SpriteNode, error)
}

type SpriteStart struct {
	Remote, Item     string
	Definition       remotedef.Definition
	DefinitionSHA256 string
}

type SpriteNode struct {
	Node   string
	Handle provider.Handle
	Root   string
}

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
		leave bool
	)
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
	leftOrEnded := func(ctx context.Context) string {
		if leave {
			return leftNote(h)
		}
		return end(ctx)
	}
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
	if b, _, err := checkoutHead(ctx, h, root, c.Head.Ref, c.Head.Commit, shallow); err != nil {
		return fail(err, string(b))
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

func leftNote(h provider.Handle) string {
	return fmt.Sprintf("the container %s is left to inspect: docker exec -it %s sh; remove it with docker rm -f %s", h.ID(), h.ID(), h.ID())
}

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

func validateSkillRel(rel string) error {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("skill directory %q must be relative to the worktree", rel)
	}
	if slices.Contains(strings.Split(rel, "/"), "..") {
		return fmt.Errorf("skill directory %q escapes the worktree", rel)
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("skill directory %q escapes the worktree", rel)
	}
	return nil
}

func resolveAbsPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		return eval, nil
	}
	return abs, nil
}

func stageGate(tree, in string, c gate.RemoteCheck) (bool, error) {
	if err := os.MkdirAll(in, 0o755); err != nil {
		return false, err
	}
	skillRel := ""
	if c.Env.SkillDir != "" {
		c.Script = gate.ResolveSkillScript(c.Script)
		treeAbs, err := resolveAbsPath(tree)
		if err != nil {
			return false, err
		}
		skillAbs, err := resolveAbsPath(c.Env.SkillDir)
		if err != nil {
			return false, err
		}
		rel, err := filepath.Rel(treeAbs, skillAbs)
		if err != nil {
			return false, fmt.Errorf("skill directory %q is outside worktree %q: %w", c.Env.SkillDir, tree, err)
		}
		skillRel = filepath.ToSlash(rel)
		if err := validateSkillRel(skillRel); err != nil {
			return false, err
		}
		c.Env.SkillDir = ""
	}
	shallow, err := stageHead(tree, in, c.Head.Ref)
	if err != nil {
		return false, err
	}
	wrapper, err := gateWrapperScript(c.Env, c.Script, skillRel)
	if err != nil {
		return false, err
	}
	for name, b := range map[string]string{
		gateScript:  c.Script,
		gatePayload: string(c.Env.Payload()),
		gateWrapper: wrapper,
	} {
		if err := os.WriteFile(filepath.Join(in, name), []byte(b), 0o644); err != nil {
			return false, err
		}
	}
	return shallow, nil
}

func gateWrapperScript(env gate.CheckEnv, script, skillRel string) (string, error) {
	if skillRel != "" {
		if err := validateSkillRel(skillRel); err != nil {
			return "", err
		}
	}
	argv, shebang, err := gate.ScriptInterpreter(script)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Runs a gate's check on this node; written by satelle.\nset -u\nroot=$1\nmkdir -p \"$root/out\" || exit 1\n")
	for _, kv := range env.Vars() {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "export %s=%s\n", k, ShQuote(v))
	}
	if skillRel != "" {
		fmt.Fprintf(&b, "export SATELLE_SKILL_DIR=\"$root/work/\"%s\n", ShQuote(skillRel))
	}
	b.WriteString("cd \"$root/work\" || exit 1\n")
	redirect := ` < "$root/in/` + gatePayload + `" > "$root/out/` + gateLog + `" 2>&1`
	if shebang {
		for _, a := range argv {
			b.WriteString(ShQuote(a) + " ")
		}
		b.WriteString(`"$root/in/` + gateScript + `"` + redirect + "\n")
	} else {
		b.WriteString(`sh "$root/in/` + gateScript + `"` + redirect + "\n")
	}
	b.WriteString(`echo $? > "$root/out/` + gateExit + `"` + "\n")
	return b.String(), nil
}

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

func ShQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
