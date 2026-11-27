package cli

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

const checkReposName = "repos"

func showOrigin(v string) string { return quoteControl(reporeg.DisplayOrigin(v)) }

func show(v string) string { return quoteControl(v) }

const (
	repoOK         = "ok"
	repoUnmapped   = "unmapped"
	repoMissing    = "missing"
	repoMismatch   = "mismatch"
	repoUnreadable = "unreadable"
	repoInvalid    = "invalid"
)

type checkout struct {
	state  string
	path   string
	actual string
}

func checkoutOf(reg reporeg.Registry, name string, env []string) checkout {
	if !reg.EntryOK(name) {
		return checkout{state: repoInvalid}
	}
	path, mapped, ok := reg.LocalPath(name)
	switch {
	case !mapped:
		return checkout{state: repoUnmapped}
	case !ok:
		return checkout{state: repoInvalid}
	}
	if _, err := os.Stat(path); err != nil {
		return checkout{state: repoMissing, path: path}
	}
	actual, err := gitfacts.Repo{Dir: path, Env: env}.RemoteURL("origin")
	if err != nil {
		return checkout{state: repoUnreadable, path: path}
	}
	want, _ := reporeg.NormaliseOrigin(reg.Tracked[name].URL)
	if got, err := reporeg.NormaliseOrigin(actual); err != nil || got != want {
		return checkout{state: repoMismatch, path: path, actual: actual}
	}
	return checkout{state: repoOK, path: path, actual: actual}
}

func declaredNames(reg reporeg.Registry) []string { return reg.Targets()[1:] }

func checkRepos(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	if in.cfg == nil || !reporeg.Exists(in.r.DataDir) {
		return nil, nil
	}
	reg := in.cfg.Repos
	out := machineFileIssues(in, checkReposName, kernel.ReposLocalFile, "a machine's checkout paths")
	remap := func(name string) string { return "satelle repo add " + name + " --path <dir>" }
	for _, name := range declaredNames(reg) {
		co := checkoutOf(reg, name, in.r.gitEnv)
		tracked := showOrigin(reg.Tracked[name].URL)
		prefix := show(name) + ": "
		repointFix := "point the checkout's origin at " + tracked + ", or map the checkout that has it (" + remap(name) + ")"
		switch co.state {
		case repoUnmapped:
			out = append(out, doctorIssue{Check: checkReposName,
				Problem: prefix + "no local checkout mapped in .satelle/" + kernel.ReposLocalFile,
				Fix:     "clone " + tracked + " on this machine, then " + remap(name)})
		case repoMissing:
			out = append(out, doctorIssue{Check: checkReposName,
				Problem: prefix + "local checkout " + show(co.path) + " does not exist",
				Fix:     "clone " + tracked + " to " + show(co.path) + " with your own git credentials, or " + remap(name) + " to map another checkout"})
		case repoUnreadable:
			out = append(out, doctorIssue{Check: checkReposName,
				Problem: prefix + show(co.path) + " has no origin remote", Fix: repointFix})
		case repoMismatch:
			out = append(out, doctorIssue{Check: checkReposName,
				Problem: prefix + show(co.path) + " has origin " + showOrigin(co.actual) + " but .satelle/" + kernel.ReposFile + " registers " + tracked,
				Fix:     repointFix})
		}
	}
	dup, notes := onboardingIssues(in, reg)
	out = append(out, dup...)
	if m := in.cfg.Doctor.Repos.MaxCount; m != nil && *m > 0 && reg.Declared() > *m {
		out = append(out, doctorIssue{Check: checkReposName,
			Problem: fmt.Sprintf("%d repositories registered, over .satelle/%s [repos] max_count = %d", reg.Declared(), kernel.DoctorFile, *m),
			Fix:     "split the control repository, or raise [repos] max_count in .satelle/" + kernel.DoctorFile})
	}
	return out, notes
}

func onboardingIssues(in *doctorInput, reg reporeg.Registry) ([]doctorIssue, []string) {
	gc, err := config.LoadGlobal()
	if err != nil || len(gc.Projects.Paths) == 0 {
		return nil, nil
	}
	env := in.r.gitEnv
	rootOf := func(p string) string { return canonical(config.CanonicalRepoRootEnv(p, env)) }
	here := rootOf(filepath.Dir(in.r.DataDir))
	mine := map[string]string{here: reporeg.Self}
	for _, name := range declaredNames(reg) {
		if p, mapped, ok := reg.LocalPath(name); reg.EntryOK(name) && mapped && ok {
			mine[canonical(p)] = name
		}
	}
	var out []doctorIssue
	var notes []string
	for _, p := range gc.Projects.Paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		root := rootOf(p)
		if root == here {
			continue
		}
		local, ok := reporeg.LoadLocalQuiet(filepath.Join(root, config.DefaultDataDir))
		if !ok {
			notes = append(notes, checkReposName+": "+show(root)+": its "+kernel.ReposLocalFile+" could not be read")
			continue
		}
		theirs := map[string]string{root: reporeg.Self}
		for name, path := range local {
			theirs[canonical(path)] = name
		}
		for _, co := range slices.Sorted(maps.Keys(mine)) {
			other, claimed := theirs[co]
			if !claimed {
				continue
			}
			out = append(out, doctorIssue{Check: checkReposName,
				Problem: fmt.Sprintf("%s is onboarded by two control repositories: %s (as %s) and %s (as %s)",
					show(co), show(here), show(mine[co]), show(root), show(other)),
				Fix: "a checkout is onboarded by one control repository: remove its entry from one .satelle/" + kernel.ReposLocalFile})
		}
	}
	return out, notes
}

func machineFileIssues(in *doctorInput, check, file, what string) []doctorIssue {
	rel, err := filepath.Rel(in.r.Tree, filepath.Join(in.r.DataDir, file))
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	g := gitfacts.Repo{Dir: in.r.Tree, Env: in.r.gitEnv}
	var out []doctorIssue
	if ignored, err := g.IgnoredBy(rel); err == nil && ignored[rel] == "" {
		out = append(out, doctorIssue{Check: check,
			Problem: "no ignore rule covers " + rel + ", so " + what + " would show as a change in git",
			Fix:     "add the line " + file + " to .satelle/.gitignore and commit it through this repo's workflow", Resolve: resolveFixItem})
	}
	if tracked, err := g.Tracks(rel); err == nil && tracked {
		out = append(out, doctorIssue{Check: check,
			Problem: rel + " is this machine's file but git tracks it",
			Fix:     "git rm --cached " + rel + " and commit, through this repo's workflow", Resolve: resolveFixItem})
	}
	return out
}
