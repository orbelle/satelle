package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

type repo struct {
	DataDir string
	Tree    string
	Runtime string
	// Node is the node id of the node run whose runtime this is; empty for
	// the machine's runtime.
	Node   string
	gitEnv []string
}

var errNotInitialised = errors.New("no .satelle directory here or above; run `satelle init`")

// findRepo resolves the command's substrate, tree and runtime, following the
// node hint a node run hands to what it starts.
func findRepo(dir string) (repo, error) { return resolveRepo(dir, os.Environ(), true) }

// findOwnRepo resolves the work tree's own substrate and never reads the node
// hint: the hooks and the session-start doctor use it.
func findOwnRepo(dir string) (repo, error) { return resolveRepo(dir, os.Environ(), false) }

type nodeRun struct {
	stamp   config.RuntimeStamp
	runtime string
}

func resolveRepo(start string, environ []string, nodeAware bool) (repo, error) {
	g, err := resolveGovernance(start, environ, nodeAware)
	if err != nil {
		return repo{}, err
	}
	return governRepo(g.substrate, g.tree, g.node, g.gitEnv)
}

type governance struct {
	substrate, tree string
	node            *nodeRun
	gitEnv          []string
}

// resolveGovernance finds the substrate and work tree a command started in
// start is governed by. The declared secrets are withheld from gitEnv before
// anything else runs; the node hint is followed only when nodeAware, and
// only into the tree its verified node runtime names.
func resolveGovernance(start string, environ []string, nodeAware bool) (governance, error) {
	gitEnv := config.GitLookupEnv(environ, splitNames(envValue(environ, config.NodeWithholdEnv)))
	if hint := envValue(environ, config.NodeRunEnv); nodeAware && hint != "" {
		stamp, runtime, err := config.NodeRuntime(hint)
		if err != nil {
			return governance{}, err
		}
		if under(canonical(startDir(start)), stamp.Tree) {
			return governance{stamp.Substrate, stamp.Tree, &nodeRun{stamp: stamp, runtime: runtime}, gitEnv}, nil
		}
	}
	dataDir, ok := config.FindDataDir(start)
	if !ok {
		return governance{}, errNotInitialised
	}
	tree := filepath.Dir(dataDir)
	if top, err := (gitfacts.Repo{Dir: canonical(tree), Env: gitEnv}).TopLevel(); err == nil && top != "" {
		tree = top
	}
	return governance{dataDir, tree, nil, gitEnv}, nil
}

// governRepo is the repo governed by substrate over the work tree tree: on
// the machine's runtime when node is nil, else on the node's verified
// runtime, where a substrate other than the tree's own .satelle must still
// match the digest the node run stamped.
func governRepo(substrate, tree string, node *nodeRun, gitEnv []string) (repo, error) {
	if node == nil {
		return repo{DataDir: substrate, Tree: canonical(tree), Runtime: config.RuntimeDirEnv(filepath.Dir(substrate), gitEnv), gitEnv: gitEnv}, nil
	}
	if err := node.matches(substrate, tree); err != nil {
		return repo{}, err
	}
	return repo{DataDir: substrate, Tree: canonical(tree), Runtime: node.runtime, Node: node.stamp.Node, gitEnv: gitEnv}, nil
}

// matches requires a substrate other than the tree's own .satelle to still
// match the digest the node run stamped.
func (n *nodeRun) matches(substrate, tree string) error {
	if canonical(substrate) == filepath.Join(canonical(tree), config.DefaultDataDir) {
		return nil
	}
	d, err := kernel.SubstrateDigest(substrate)
	if err != nil {
		return fmt.Errorf("the node's substrate %s: %w", substrate, err)
	}
	if d != n.stamp.Digest {
		return fmt.Errorf("the node's substrate %s does not match the digest its node run stamped", substrate)
	}
	return nil
}

func startDir(start string) string {
	if strings.TrimSpace(start) != "" {
		return start
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func under(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func envValue(environ []string, name string) string {
	v := ""
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == name {
			v = val
		}
	}
	return strings.TrimSpace(v)
}

func splitNames(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func openKernel() (*kernel.Kernel, repo, error) {
	r, err := findRepo("")
	if err != nil {
		return nil, r, err
	}
	k, err := r.open()
	return k, r, err
}

const dryRunReadOnly = "a dry run (satelle gate run) started this command: it may read the ledger, not write it"

func (r repo) open() (*kernel.Kernel, error) {
	readOnly := ""
	if kernel.UnderDryRun(r.Runtime) {
		readOnly = dryRunReadOnly
	}
	return r.openWith(readOnly)
}

func (r repo) openWith(readOnly string) (*kernel.Kernel, error) {
	if err := os.MkdirAll(r.Runtime, 0o755); err != nil {
		return nil, err
	}
	if r.Node == "" {
		_ = config.WriteRepoPathMarkerEnv(r.Runtime, filepath.Dir(r.DataDir), r.gitEnv)
		_ = config.MarkMachineRuntime(r.Runtime)
	}
	k, err := kernel.Open(kernel.Options{Root: r.DataDir, Runtime: r.Runtime, Dir: r.Tree, ReadOnly: readOnly})
	if err != nil {
		return nil, fmt.Errorf("%w\nrun `satelle validate` for every problem", err)
	}
	sprites := remotenode.ServerSprites{Placer: func(context.Context) (remotenode.NodePlacer, func(), error) {
		p, closeFn, ok, err := hostedPlacer(k, r)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, errNotBound
		}
		return p, closeFn, nil
	}}
	home := config.GlobalDir()
	k.Machine, _ = config.MachineID(home)
	k.Host, _ = os.Hostname()
	harness, _ := sessionMeta(home, sessionFromEnv())
	if err := k.SetOrigin(k.Machine, ledger.EnvRole(config.WorkerFromEnv()), harness); err != nil {
		k.Close()
		return nil, err
	}
	k.User = func() string { return seatUser(k.Host) }
	k.Sessions = seatlife.Recorder{Home: home}
	k.SessionMeta = func(session string) (string, string) { return sessionMeta(home, session) }
	k.RemoteChecks = remotenode.GateChecker{Tree: r.Tree, DataDir: r.DataDir, Docker: provider.DockerLocal{Egress: firingBinary()}, Sprites: sprites, Out: os.Stderr}
	if r.Node == "" {
		watchWrites(k, r)
		k.Progress = func(id string) { pushItem(r, id) }
	}
	return k, nil
}

func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p)
}

func sessionFromEnv() string { return config.SessionFromEnv() }

func sessionMeta(home, session string) (harness, model string) {
	if rec, ok := seatlife.Load(home, session); ok {
		harness, model = rec.Harness, rec.Model
	}
	if harness == "" {
		harness = hook.Caller()
	}
	return harness, model
}

func seatUser(host string) string {
	if u := signedInUser(); u != nil {
		if u.Email != "" {
			return u.Email
		}
		return u.Name
	}
	u := osUser()
	if u == "" {
		return ""
	}
	return u + "@" + host
}
