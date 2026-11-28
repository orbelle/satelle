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
	Node    string
	gitEnv  []string
}

var errNotInitialised = errors.New("no .satelle directory here or above; run `satelle init`")

func findRepo(dir string) (repo, error) { return resolveRepo(dir, os.Environ(), true) }

func findOwnRepo(dir string) (repo, error) { return resolveRepo(dir, os.Environ(), false) }

type nodeRun struct {
	stamp   config.RuntimeStamp
	runtime string
}

func substrateFor(start string, environ []string, nodeAware bool) (string, *nodeRun, error) {
	if hint := envValue(environ, config.NodeRunEnv); nodeAware && hint != "" {
		stamp, runtime, err := config.NodeRuntime(hint)
		if err != nil {
			return "", nil, err
		}
		if under(canonical(startDir(start)), stamp.Tree) {
			if err := checkNodeSubstrate(stamp); err != nil {
				return "", nil, err
			}
			return stamp.Substrate, &nodeRun{stamp: stamp, runtime: runtime}, nil
		}
	}
	dataDir, ok := config.FindDataDir(start)
	if !ok {
		return "", nil, errNotInitialised
	}
	return dataDir, nil, nil
}

func checkNodeSubstrate(stamp config.RuntimeStamp) error {
	if canonical(stamp.Substrate) == filepath.Join(canonical(stamp.Tree), config.DefaultDataDir) {
		return nil
	}
	d, err := kernel.SubstrateDigest(stamp.Substrate)
	if err != nil {
		return fmt.Errorf("%w: the node's substrate %s: %w", config.ErrNodeHint, stamp.Substrate, err)
	}
	if d != stamp.Digest {
		return fmt.Errorf("%w: the node's substrate %s: its substrate digest does not match the one its node run stamped", config.ErrNodeHint, stamp.Substrate)
	}
	return nil
}

func resolveRepo(start string, environ []string, nodeAware bool) (repo, error) {
	gitEnv := config.GitLookupEnv(environ, config.WithheldNames(environ))
	substrate, node, err := substrateFor(start, environ, nodeAware)
	if err != nil {
		return repo{}, err
	}
	if node != nil {
		return governRepo(substrate, node.stamp.Tree, node, gitEnv)
	}
	tree := filepath.Dir(substrate)
	if top, err := (gitfacts.Repo{Dir: canonical(tree), Env: gitEnv}).TopLevel(); err == nil && top != "" {
		tree = top
	}
	return governRepo(substrate, tree, nil, gitEnv)
}

func governRepo(substrate, tree string, node *nodeRun, gitEnv []string) (repo, error) {
	if node == nil {
		return repo{DataDir: substrate, Tree: canonical(tree), Runtime: config.RuntimeDirEnv(filepath.Dir(substrate), gitEnv), gitEnv: gitEnv}, nil
	}
	if err := checkNodeSubstrate(node.stamp); err != nil {
		return repo{}, err
	}
	return repo{DataDir: substrate, Tree: canonical(tree), Runtime: node.runtime, Node: node.stamp.Node, gitEnv: gitEnv}, nil
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
		_ = config.WriteRepoPathMarker(r.Runtime, filepath.Dir(r.DataDir), r.gitEnv)
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
