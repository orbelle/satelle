package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// interface = "cloud" (sty_82cffd60): a fourth dispatch transport, valid only on
// a role=agent performer binding, never a live interface, and the one place
// collect_doc is meaningful.

func loadAgentsText(t *testing.T, body string) (AgentsConfig, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadAgents(dir)
}

func TestCloudInterfaceResolves(t *testing.T) {
	if got := (AgentBinding{Interface: "cloud"}).ResolvedInterface(); got != InterfaceCloud {
		t.Errorf("cloud = %q", got)
	}
	if got := (AgentBinding{Interface: " Cloud "}).ResolvedInterface(); got != InterfaceCloud {
		t.Errorf("cloud (spaced, cased) = %q", got)
	}
}

func TestCloudInterfaceLoadsOnAPerformer(t *testing.T) {
	ac, err := loadAgentsText(t, "[coder]\nrole = \"agent\"\ninterface = \"cloud\"\ncommand = \"claude -p {system}\"\ncollect_doc = \"ac-evidence\"\ntimeout = \"45m\"\n")
	if err != nil {
		t.Fatalf("a role=agent cloud binding must load: %v", err)
	}
	b := ac.Agents["coder"]
	if b.ResolvedInterface() != InterfaceCloud || b.CollectDoc != "ac-evidence" {
		t.Errorf("binding = %+v", b)
	}
	// A section with no role= is inferred an agent unless it is named reviewer.
	if _, err := loadAgentsText(t, "[builder]\ninterface = \"cloud\"\ncommand = \"claude -p {system}\"\n"); err != nil {
		t.Errorf("an inferred-agent cloud binding must load: %v", err)
	}
}

func TestCloudInterfaceRefusedOnJudgesAndConsultants(t *testing.T) {
	for name, body := range map[string]string{
		"reviewer section":      "[reviewer]\ninterface = \"cloud\"\ncommand = \"claude -p {system}\"\n",
		"role=reviewer binding": "[judge]\nrole = \"reviewer\"\ninterface = \"cloud\"\ncommand = \"claude -p {system}\"\n",
	} {
		_, err := loadAgentsText(t, body)
		if err == nil || !strings.Contains(err.Error(), "cloud") || !strings.Contains(err.Error(), "role=agent") {
			t.Errorf("%s: err = %v, want a refusal naming cloud and role=agent", name, err)
		}
	}
}

func TestCollectDocOnlyOnACloudBinding(t *testing.T) {
	for _, iface := range []string{"", "command", "acp", "stream"} {
		line := ""
		if iface != "" {
			line = "interface = \"" + iface + "\"\n"
		}
		_, err := loadAgentsText(t, "[coder]\nrole = \"agent\"\n"+line+"command = \"claude -p {system}\"\ncollect_doc = \"ac-evidence\"\n")
		if err == nil || !strings.Contains(err.Error(), "collect_doc") {
			t.Errorf("interface %q: err = %v, want a collect_doc refusal", iface, err)
		}
	}
}

// Cloud is never a live transport: the default order excludes it, and the order
// cannot be configured to include it.
func TestCloudIsNeverALiveInterface(t *testing.T) {
	for _, iface := range (AgentsDefaults{}).LiveInterfaces() {
		if iface == InterfaceCloud {
			t.Fatal("the default live interfaces include cloud")
		}
	}
	if got := (AgentsDefaults{LiveInterfaceOrder: []string{InterfaceACP}}).LiveInterfaces(); len(got) != 1 || got[0] != InterfaceACP {
		t.Errorf("configured order = %v", got)
	}
	_, err := loadAgentsText(t, "[defaults]\nlive_interfaces = [\"cloud\"]\n")
	if err == nil || !strings.Contains(err.Error(), "live_interfaces") {
		t.Errorf("live_interfaces = [cloud]: err = %v, want a refusal", err)
	}
	// A cloud binding never resolves to a live transport either.
	b := AgentBinding{Role: RoleAgent, Interface: "cloud", Command: "claude -p {system}"}
	if iface, _ := (AgentsConfig{}).ResolveInterface(b, UseLive); iface != InterfaceCloud {
		t.Errorf("explicit cloud resolved for live use to %q", iface)
	}
}
