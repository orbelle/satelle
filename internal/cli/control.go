package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

type onboarding struct {
	root  string
	local map[string]string
	ok    bool
}

func onboardings(env []string) []onboarding {
	gc, err := config.LoadGlobal()
	if err != nil {
		return nil
	}
	var out []onboarding
	for _, p := range gc.Projects.Paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		root := canonical(config.CanonicalRepoRootEnv(p, env))
		local, ok := reporeg.LoadLocalQuiet(filepath.Join(root, config.DefaultDataDir))
		out = append(out, onboarding{root: root, local: local, ok: ok})
	}
	return out
}

type controlMatch struct{ root, name string }

func controlFor(start string, env []string) (root, name, tree string, err error) {
	g := gitfacts.Repo{Dir: start, Env: env}
	top, terr := g.TopLevel()
	common, cerr := g.CommonDir()
	if terr != nil || cerr != nil || top == "" || common == "" {
		return "", "", "", errNotInitialised
	}
	tree, common = canonical(top), canonical(common)
	var found []controlMatch
	for _, o := range onboardings(env) {
		if !o.ok {
			continue
		}
		for n, p := range o.local {
			m := controlMatch{root: o.root, name: n}
			if slices.Contains(found, m) || !sameCheckout(canonical(p), tree, common, env) {
				continue
			}
			reg, lerr := reporeg.Load(filepath.Join(o.root, config.DefaultDataDir))
			if lerr != nil || !reg.EntryOK(n) || checkoutOf(reg, n, env).state != repoOK {
				continue
			}
			found = append(found, m)
		}
	}
	switch len(found) {
	case 0:
		return "", "", "", errNotInitialised
	case 1:
		return found[0].root, found[0].name, tree, nil
	}
	return "", "", "", fmt.Errorf("%s is onboarded by two control repositories: %s (as %s) and %s (as %s); a checkout is onboarded by one control repository: remove its entry from one .satelle/%s",
		show(tree), show(found[0].root), show(found[0].name), show(found[1].root), show(found[1].name), kernel.ReposLocalFile)
}

func sameCheckout(mapped, tree, common string, env []string) bool {
	if mapped == tree {
		return true
	}
	c, err := gitfacts.Repo{Dir: mapped, Env: env}.CommonDir()
	return err == nil && c != "" && canonical(c) == common
}

type targetView struct {
	control repo
	Tree    string
	Target  string
}

// resolveTarget answers the control directory, work tree and target for a
// start directory: an initialised repository is its own target (self), and a
// checkout a control repository on this machine maps in its
// .satelle/repos.local.toml is that control's named target. Only a read-only
// allowlist calls it: statusCmd, sessionContext and loadDoctorInput. Every
// other command resolves through findRepo or findOwnRepo, which do not resolve
// a mapped target; a new caller is reviewed against this list.
func resolveTarget(start string, nodeAware bool) (targetView, error) {
	environ := os.Environ()
	r, err := resolveRepo(start, environ, nodeAware)
	if err == nil {
		return targetView{control: r, Tree: r.Tree, Target: reporeg.Self}, nil
	}
	if !errors.Is(err, errNotInitialised) || envValue(environ, config.NodeRunEnv) != "" {
		return targetView{}, err
	}
	gitEnv := config.GitLookupEnv(environ, splitNames(envValue(environ, config.NodeWithholdEnv)))
	root, name, tree, cerr := controlFor(canonical(startDir(start)), gitEnv)
	if cerr != nil {
		if errors.Is(cerr, errNotInitialised) {
			return targetView{}, err
		}
		return targetView{}, cerr
	}
	ctl, rerr := resolveRepo(root, environ, false)
	if rerr != nil {
		return targetView{}, rerr
	}
	if canonical(ctl.DataDir) != filepath.Join(root, config.DefaultDataDir) {
		return targetView{}, err
	}
	return targetView{control: ctl, Tree: tree, Target: name}, nil
}

func (v targetView) mapped() bool { return v.Target != reporeg.Self }

const readOnlyAtTarget = "a mapped target work tree: satelle reads the control repository here, it does not write it"

func (v targetView) open() (*kernel.Kernel, error) {
	k, err := kernel.Open(kernel.Options{Root: v.control.DataDir, Runtime: v.control.Runtime, Dir: v.control.Tree, Target: v.Target, ReadOnly: readOnlyAtTarget})
	if err != nil {
		return nil, fmt.Errorf("%w\nrun `satelle -C %s validate` for every problem", err, v.control.Tree)
	}
	return k, nil
}

func (r repo) control() string {
	return filepath.Join(canonical(config.CanonicalRepoRootEnv(filepath.Dir(r.DataDir), r.gitEnv)), config.DefaultDataDir)
}

func targetLine(v targetView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "satelle: this work tree (%s) is target %s of the control repository %s; ", show(v.Tree), show(v.Target), show(v.control.Tree))
	fmt.Fprintf(&b, "here satelle reads status, session context and doctor only, and gates no edit: run every other satelle command from the control repository (satelle -C %s <command>).\n", show(v.control.Tree))
	return b.String()
}
