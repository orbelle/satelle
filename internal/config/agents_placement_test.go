package config

import (
	"strings"
	"testing"
)

// placement = "local" | "remote" | "team:<path>" (sty_7028d996): declared on the
// binding, default local, refused at load for anything else.

func TestResolvedPlacement(t *testing.T) {
	cases := []struct {
		in       string
		wantKind string
		wantPath string
		label    string
	}{
		{"", PlacementLocal, "", "local"},
		{"local", PlacementLocal, "", "local"},
		{"REMOTE", PlacementRemote, "", "remote"},
		{" remote ", PlacementRemote, "", "remote"},
		{"team:acme/ops", PlacementTeam, "acme/ops", "team:acme/ops"},
		{"Team:Acme/Ops", PlacementTeam, "Acme/Ops", "team:Acme/Ops"},
		{"team:", "team:", "", "team:"},
		{"bogus", "bogus", "", "bogus"},
	}
	for _, tc := range cases {
		b := AgentBinding{Placement: tc.in}
		kind, path := b.ResolvedPlacement()
		if kind != tc.wantKind || path != tc.wantPath {
			t.Errorf("ResolvedPlacement(%q) = (%q, %q), want (%q, %q)", tc.in, kind, path, tc.wantKind, tc.wantPath)
		}
		if got := b.PlacementLabel(); got != tc.label {
			t.Errorf("PlacementLabel(%q) = %q, want %q", tc.in, got, tc.label)
		}
	}
}

const placementCmd = "command = \"claude -p {system}\"\n"

func TestPlacementLoadsInTheRepoFile(t *testing.T) {
	ac, err := loadAgentsText(t, strings.Join([]string{
		"[plain]", placementCmd,
		"[far]", "placement = \"remote\"", "remote = \"r1\"", placementCmd,
		"[team]", "placement = \"team:acme/ops\"", placementCmd,
		"[near]", "placement = \"local\"", placementCmd,
	}, "\n"))
	if err != nil {
		t.Fatalf("valid placements must load: %v", err)
	}
	for name, want := range map[string]string{"plain": "local", "far": "remote", "team": "team:acme/ops", "near": "local"} {
		if got := ac.Agents[name].PlacementLabel(); got != want {
			t.Errorf("[%s] placement = %q, want %q", name, got, want)
		}
	}
	if ac.Agents["far"].Remote != "r1" {
		t.Errorf("remote = %q, want r1", ac.Agents["far"].Remote)
	}
}

func TestRemotePathAcceptedWithARemotePlacement(t *testing.T) {
	ac, err := loadAgentsText(t, "[far]\nplacement = \"remote\"\nremote = \"r1\"\nremote_path = \"remotes/step-pi\"\n"+placementCmd)
	if err != nil {
		t.Fatalf("remote_path with placement=remote must load: %v", err)
	}
	if got := ac.Agents["far"].RemotePath; got != "remotes/step-pi" {
		t.Errorf("remote_path = %q, want remotes/step-pi", got)
	}
	if _, err := ParseGlobalAgents("[profiles.p]\nrole = \"agent\"\nplacement = \"remote\"\nremote = \"r\"\nremote_path = \"remotes/x\"\n"); err != nil {
		t.Errorf("a catalog profile may declare remote_path: %v", err)
	}
	if _, err := ParseGlobalAgents("[profiles.p]\nrole = \"agent\"\nremote_path = \"remotes/x\"\n"); err == nil {
		t.Error("a catalog remote_path without placement=remote must be refused")
	}
	out, _ := overlayBinding(AgentBinding{}, nil, AgentBinding{Placement: "remote", Remote: "r1", RemotePath: "remotes/x"}, "T")
	if out.RemotePath != "remotes/x" {
		t.Errorf("overlay dropped remote_path: %+v", out)
	}
}

func TestPlacementRefusedInTheRepoFile(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"unknown value":        {"placement = \"bogus\"\n", "placement=bogus is not local|remote|team:<path>"},
		"team without a path":  {"placement = \"team:\"\n", "placement=team: is not local|remote|team:<path>"},
		"remote names nothing": {"placement = \"remote\"\n", "placement=remote names no remote"},
		"remote on the cloud":  {"placement = \"remote\"\nremote = \"r1\"\ninterface = \"cloud\"\n", "cannot be combined with interface=cloud"},
		"remote= on a local":   {"remote = \"r1\"\n", "only a placement=remote binding names a remote"},
		"remote= on a team":    {"placement = \"team:a/b\"\nremote = \"r1\"\n", "only a placement=remote binding names a remote"},
		"remote_path on local": {"remote_path = \"remotes/step-pi\"\n", "only a placement=remote binding names a remote definition path"},
		"remote_path on team":  {"placement = \"team:a/b\"\nremote_path = \"remotes/step-pi\"\n", "only a placement=remote binding names a remote definition path"},
	}
	for name, tc := range cases {
		_, err := loadAgentsText(t, "[coder]\nrole = \"agent\"\n"+placementCmd+tc.body)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

// A team placement runs a task and carries no verdict, so a reviewer binding that
// declares one is refused at load, naming the binding (sty_f5cae2e5); a reviewer
// uses placement = remote, and a team placement stays valid for a performer.
func TestTeamPlacementRefusedForAReviewerBinding(t *testing.T) {
	_, err := loadAgentsText(t, "[step-pi]\nrole = \"reviewer\"\nplacement = \"team:acme/ops\"\n"+placementCmd)
	if err == nil || !strings.Contains(err.Error(), "[step-pi]") || !strings.Contains(err.Error(), "carries no verdict") {
		t.Fatalf("err = %v, want a refusal naming [step-pi] and that a team placement carries no verdict", err)
	}
	if _, err := loadAgentsText(t, "[step-pi]\nrole = \"reviewer\"\nplacement = \"remote\"\nremote = \"rmt_1\"\n"+placementCmd); err != nil {
		t.Errorf("a reviewer with placement = remote must load: %v", err)
	}
	if _, err := loadAgentsText(t, "[worker]\nrole = \"agent\"\nplacement = \"team:acme/ops\"\n"+placementCmd); err != nil {
		t.Errorf("a performer with a team placement must load: %v", err)
	}
	_, err = ParseGlobalAgents("[profiles.p]\nrole = \"reviewer\"\nplacement = \"team:acme/ops\"\n")
	if err == nil || !strings.Contains(err.Error(), "carries no verdict") {
		t.Errorf("catalog reviewer with a team placement: err = %v, want the same refusal", err)
	}
}

func TestPlacementInTheCatalog(t *testing.T) {
	g, err := ParseGlobalAgents("[profiles.p]\nrole = \"agent\"\nplacement = \"team:acme/ops\"\n")
	if err != nil {
		t.Fatalf("a catalog profile may declare a placement: %v", err)
	}
	if got := g.Profiles["p"].PlacementLabel(); got != "team:acme/ops" {
		t.Errorf("placement = %q", got)
	}
	for name, body := range map[string]string{
		"unknown":        "placement = \"bogus\"\n",
		"team no path":   "placement = \"team:\"\n",
		"remote no id":   "placement = \"remote\"\n",
		"remote + cloud": "placement = \"remote\"\nremote = \"r\"\ninterface = \"cloud\"\n",
	} {
		_, err := ParseGlobalAgents("[profiles.p]\nrole = \"agent\"\n" + body)
		if err == nil || !strings.Contains(err.Error(), "placement") {
			t.Errorf("catalog %s: err = %v, want a placement refusal", name, err)
		}
	}
}

func TestOverlayBindingCarriesPlacement(t *testing.T) {
	out, src := overlayBinding(AgentBinding{Model: "m"}, nil, AgentBinding{Placement: "remote", Remote: "r1"}, "T")
	if out.Placement != "remote" || out.Remote != "r1" || out.Model != "m" || src["placement"] != "T" {
		t.Fatalf("overlay = %+v %v", out, src)
	}
	out, _ = overlayBinding(out, src, AgentBinding{}, "U")
	if out.Placement != "remote" {
		t.Errorf("an empty tier must not clear placement: %+v", out)
	}
}

// trigger = "<request trigger>" names the declared request trigger a step is
// woken through when it is placed on a team remote (sty_1f6e6667). It is
// accepted on a binding of any placement (a pin can place a local-bound step on
// a team), carried through the overlay, and refused when present but blank.
func TestTriggerLoadsOnABinding(t *testing.T) {
	ac, err := loadAgentsText(t, strings.Join([]string{
		"[pr-remote]", "role = \"agent\"", "placement = \"team:acme/ops\"", "trigger = \"pr-approve-request\"", placementCmd,
		"[coder]", "role = \"agent\"", "trigger = \"coder-request\"", placementCmd,
	}, "\n"))
	if err != nil {
		t.Fatalf("trigger on a team and a local binding must load: %v", err)
	}
	if got := ac.Agents["pr-remote"].Trigger; got != "pr-approve-request" {
		t.Errorf("[pr-remote] trigger = %q, want pr-approve-request", got)
	}
	if got := ac.Agents["coder"].Trigger; got != "coder-request" {
		t.Errorf("[coder] trigger = %q, want coder-request", got)
	}
	if _, err := ParseGlobalAgents("[profiles.p]\nrole = \"agent\"\nplacement = \"team:acme/ops\"\ntrigger = \"t\"\n"); err != nil {
		t.Errorf("a catalog profile may declare a trigger: %v", err)
	}
	out, src := overlayBinding(AgentBinding{}, nil, AgentBinding{Placement: "team:acme/ops", Trigger: "pr-approve-request"}, "T")
	if out.Trigger != "pr-approve-request" || src["trigger"] != "T" {
		t.Errorf("overlay dropped trigger: %+v %v", out, src)
	}
}

func TestBlankTriggerRefusedNamingFileAndSection(t *testing.T) {
	_, err := loadAgentsText(t, "[pr-remote]\nrole = \"agent\"\nplacement = \"team:acme/ops\"\ntrigger = \"  \"\n"+placementCmd)
	if err == nil {
		t.Fatal("a blank-but-present trigger must be refused")
	}
	for _, want := range []string{AgentsConfigName, "[pr-remote]", "trigger is blank"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
	_, err = ParseGlobalAgents("[profiles.p]\nrole = \"agent\"\ntrigger = \" \"\n")
	if err == nil || !strings.Contains(err.Error(), GlobalAgentsLabel) || !strings.Contains(err.Error(), "trigger is blank") {
		t.Errorf("catalog blank trigger: err = %v, want a refusal naming %s", err, GlobalAgentsLabel)
	}
}
