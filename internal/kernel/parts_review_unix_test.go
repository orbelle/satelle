//go:build !windows

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const sharedReview = "[review]\ntier_tag = \"risk\"\n[review.panels.review]\nhigh = [\"helper\"]\n"

const partReviewRule = "[[workflows.rule]]\nworkflow = \"infra\"\ncategory = \"c\"\n"

const partReviewPanel = "[review.panels.review]\nhigh = [\"reviewer\"]\n"

func partReviewRoot(t *testing.T, partSettings string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, SettingsFile), "[workflows]\ndefault = \"flow\"\n"+sharedReview)
	write(t, filepath.Join(root, AgentsFile), sharedAgents)
	write(t, filepath.Join(root, SkillsDir, "do.md"), "Do the work.\n")
	write(t, filepath.Join(root, SkillsDir, "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, WorkflowsDir, "flow.toml"), sharedFlow)
	write(t, filepath.Join(root, reporeg.TrackedFile), "[repos.x]\norigin = \"https://github.com/o/x\"\n[repos.y]\norigin = \"https://github.com/o/y\"\n")
	hostGitFloor(t)
	local := ""
	for _, r := range []string{"x", "y"} {
		tgt := t.TempDir()
		gittest.Git(t, tgt, "init", "-q", "-b", "main")
		gittest.Git(t, tgt, "remote", "add", "origin", "https://github.com/o/"+r)
		gittest.Git(t, tgt, "update-ref", "refs/remotes/origin/main", commitFile(t, tgt, "a.go", "package a\n"))
		local += "[repos." + r + "]\npath = " + strconv.Quote(tgt) + "\n"
	}
	write(t, filepath.Join(root, reporeg.LocalFile), local)
	write(t, filepath.Join(root, PartsDir, "x", AgentsFile), partAgentsFile)
	write(t, filepath.Join(root, PartsDir, "x", WorkflowsDir, "infra.toml"), partAgentsInfra)
	write(t, filepath.Join(root, PartsDir, "x", SettingsFile), partSettings)
	return root
}

func openPartReview(t *testing.T, root string) (*Kernel, *fakeDispatcher) {
	t.Helper()
	k, d := openPartAgents(t, root)
	d.answers["helper"] = accept
	return k, d
}

func crossReview(t *testing.T, k *Kernel, d *fakeDispatcher, repo, wf, to, wantCall, wantModel string) {
	t.Helper()
	f := item.Fields{Kind: ptr("story"), Title: ptr("a"), Tags: &[]string{"risk:high"}}
	if repo != "" {
		f.Repo = ptr(repo)
	}
	if wf != "" {
		f.Workflow = ptr(wf)
	} else {
		f.Category = ptr("c")
	}
	it, err := k.Create(f, "me")
	if err != nil {
		t.Fatalf("create %s: %v", repo, err)
	}
	res, err := k.Transition(context.Background(), it.ID, to, "me")
	if err != nil || !res.Moved {
		t.Fatalf("%s on %s: transition %+v %v", repo, it.Workflow, res, err)
	}
	if call, model := lastModel(t, d); call != wantCall || model != wantModel {
		t.Errorf("%s on %s dispatched %s %s, want %s %s", repo, it.Workflow, call, model, wantCall, wantModel)
	}
	if len(res.Gates) != 1 || res.Gates[0].Tier != "high" || "judge:"+res.Gates[0].Binding != wantCall {
		t.Errorf("%s on %s gate results %+v", repo, it.Workflow, res.Gates)
	}
}

func TestPartReviewPanelsJudgeTheirRepository(t *testing.T) {
	root := partReviewRoot(t, partReviewRule+partReviewPanel)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := cfg.Parts["x"].Problems; len(ps) > 0 {
		t.Fatalf("part problems: %v", errors.Join(ps...))
	}
	k, d := openPartReview(t, root)
	crossReview(t, k, d, "x", "", "build", "judge:reviewer", "x-rev")
	crossReview(t, k, d, "x", "flow", "closed", "judge:reviewer", "shared-rev")
	crossReview(t, k, d, "", "flow", "closed", "judge:helper", "shared-helper")
	crossReview(t, k, d, "self", "flow", "closed", "judge:helper", "shared-helper")
	crossReview(t, k, d, "y", "flow", "closed", "judge:helper", "shared-helper")

	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("d"), Repo: ptr("x"), Workflow: ptr("flow"), Tags: &[]string{"risk:high"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	dry, err := k.GateDry(context.Background(), GateDryRequest{Workflow: "flow", Gate: "review", Item: it.ID, ShowInput: true})
	if err != nil || dry.Input == nil || strings.Join(dry.Input.Judges, ",") != "reviewer" || dry.Input.Tier != "high" {
		t.Errorf("dry input for x: %+v %v", dry.Input, err)
	}
	if errs := Validate(root); len(errs) > 0 {
		t.Errorf("validate: %v", errors.Join(errs...))
	}
	if errs := k.Validate(); len(errs) > 0 {
		t.Errorf("kernel validate: %v", errors.Join(errs...))
	}
}

func TestPartReviewOtherRepositoryWithWorkflowsOnly(t *testing.T) {
	root := partReviewRoot(t, partReviewRule+partReviewPanel)
	write(t, filepath.Join(root, PartsDir, "y", SettingsFile), "[workflows]\n")
	k, d := openPartReview(t, root)
	if ps := k.Unresolved["y"]; len(ps) > 0 {
		t.Fatalf("y problems: %v", errors.Join(ps...))
	}
	crossReview(t, k, d, "y", "flow", "closed", "judge:helper", "shared-helper")
}

func TestPartReviewKeepsSharedPanelsForOtherGates(t *testing.T) {
	root := partReviewRoot(t, partReviewRule)
	write(t, filepath.Join(root, WorkflowsDir, "flow.toml"), sharedFlow+"[gates.extra]\nskill = \"review\"\nagent = \"reviewer\"\n")
	write(t, filepath.Join(root, PartsDir, "x", SettingsFile), partReviewRule+"[review.panels.extra]\nhigh = [\"reviewer\"]\n")
	k, d := openPartReview(t, root)
	if ps := k.Unresolved["x"]; len(ps) > 0 {
		t.Fatalf("x problems: %v", errors.Join(ps...))
	}
	crossReview(t, k, d, "x", "flow", "closed", "judge:helper", "shared-helper")
	crossReview(t, k, d, "x", "", "build", "judge:helper", "shared-helper")
}

func TestReviewForScopesByTarget(t *testing.T) {
	shared := workflow.Review{TierTag: "risk", Panels: map[string]map[string][]string{"review": {"high": {"helper"}}}}
	part := workflow.Review{Panels: map[string]map[string][]string{"review": {"high": {"reviewer"}}}}
	parts := map[string]*Part{"x": {Name: "x", Review: part}, "e": {Name: "e"}, "bad": {Name: "bad", Review: part, Problems: []error{errors.New("bad")}}}
	for _, target := range []string{reporeg.Self, "y", "e", "bad"} {
		if got := reviewFor(shared, parts, target); got.Panels["review"]["high"][0] != "helper" {
			t.Errorf("target %s: %+v, want the shared review", target, got)
		}
	}
	if got := reviewFor(shared, parts, "x"); got.Panels["review"]["high"][0] != "reviewer" || got.TierTag != "risk" {
		t.Errorf("target x: %+v", got)
	}
}

func TestPartReviewUnresolvedRefuses(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.toml")
	write(t, outside, partReviewRule+partReviewPanel)
	for _, c := range []struct {
		name, settings, want string
		link                 bool
	}{
		{"unknown key", partReviewRule + "[review]\ntier = \"high\"\n", "unknown key review.tier", false},
		{"ghost agent", partReviewRule + "[review.panels.review]\nhigh = [\"ghost\"]\n", `agent "ghost" is not in the agents catalog`, false},
		{"tier tag differs", partReviewRule + "[review]\ntier_tag = \"size\"\n" + partReviewPanel, `tier_tag "size" differs from the shared satelle.toml tier_tag "risk"`, false},
		{"no such gate", partReviewRule + "[review.panels.nogate]\nhigh = [\"reviewer\"]\n", `no workflow has a gate "nogate"`, false},
		{"part-only agent on a shared gate", partReviewRule + "[review.panels.review]\nhigh = [\"partonly\"]\n", `agent "partonly" is not in the shared agents.toml, which shared workflow flow dispatches gate "review" from`, false},
		{"symlink", "", "satelle.toml", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := partReviewRoot(t, partReviewRule)
			write(t, filepath.Join(root, PartsDir, "x", AgentsFile), partAgentsFile+"[agents.partonly]\ncommand = \"claude\"\nmodel = \"x-only\"\ntools = [\"Read\"]\n")
			k0, err := Open(Options{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			xs, err := k0.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Repo: ptr("x"), Workflow: ptr("flow"), Tags: &[]string{"risk:high"}}, "me")
			if err != nil {
				t.Fatal(err)
			}
			k0.Close()
			path := filepath.Join(root, PartsDir, "x", SettingsFile)
			if c.link {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, path, c.settings)
			}
			cfg, err := LoadConfig(root)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if ps := cfg.Problems(); len(ps) > 0 {
				t.Fatalf("shared problems: %v", errors.Join(ps...))
			}
			msg := errors.Join(cfg.Unresolved()["x"]...)
			if msg == nil || !strings.Contains(msg.Error(), "repos/x/satelle.toml") || !strings.Contains(msg.Error(), c.want) {
				t.Fatalf("part problems %v, want %q under repos/x/satelle.toml", msg, c.want)
			}
			k, d := openPartReview(t, root)
			var u *PartUnresolved
			if _, err := k.Transition(context.Background(), xs.ID, "closed", "me"); !errors.As(err, &u) {
				t.Errorf("transition of an x story: %v", err)
			}
			if len(d.calls) > 0 {
				t.Errorf("an unresolved part dispatched %v", d.calls)
			}
			crossReview(t, k, d, "", "flow", "closed", "judge:helper", "shared-helper")
			if errs := Validate(root); len(errs) > 0 {
				t.Errorf("validate names a shared problem for a part one: %v", errors.Join(errs...))
			}
		})
	}
}
