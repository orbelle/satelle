//go:build clislow

package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// TestSlowHookWrapperRows drives the generated wrapper for every wired harness. For
// claude, grok and pi the expected bytes are the pre-change literals; cursor's
// expectations come from agentcli.PreToolUseDeny.
func TestSlowHookWrapperRows(t *testing.T) {
	repo := t.TempDir()
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}

	denyOf := func(harness string) string { return string(agentcli.PreToolUseDeny(harness, "policy R")) }
	infraOf := func(harness string) string { return string(agentcli.PreToolUseDeny(harness, preChangeInfraReason)) }
	edit := func(h string) string { return hookEditEvent(h) }
	bash := func(cmd string) func(string) string {
		return func(h string) string { return hookBashEvent(h, cmd) }
	}
	silent := func(string) string { return "" }
	line := func(f func(string) string) func(string) string {
		return func(h string) string { return f(h) + "\n" }
	}

	rows := []wrapperRow{
		{"gate deny exit1", "gate", edit, "y", "", 1, nil},
		{"gate deny exit0", "gate", edit, "y", "", 0, nil},
		{"gate empty output, exit 1", "gate", edit, "y", "", 1, line(infraOf)},
		{"gate malformed output, exit 0", "gate", edit, "y", "oops\n", 0, line(infraOf)},
		{"gate malformed output, exit 1", "gate", edit, "y", "oops\n", 1, line(infraOf)},
		{"gate no binary", "gate", edit, "", "", 0, line(infraOf)},
		{"gate allow", "gate", edit, "y", "", 0, silent},
		{"commitgate allow", "commitgate", bash("echo hello"), "y", "", 0, silent},
		{"commitgate non-mutating infra failure", "commitgate", bash("ls"), "y", "", 1, silent},
		{"commitgate non-mutating, no binary", "commitgate", bash("ls"), "", "", 0, silent},
		{"commitgate commit infra failure", "commitgate", bash("git commit -m x"), "y", "", 1, line(infraOf)},
		{"commitgate push infra failure", "commitgate", bash("git push"), "y", "oops\n", 0, line(infraOf)},
		{"commitgate commit, no binary", "commitgate", bash("git commit -m x"), "", "", 0, line(infraOf)},
	}

	for _, harness := range []string{"claude", "grok", "pi", "cursor"} {
		for _, r := range rows {
			t.Run(harness+"/"+r.name, func(t *testing.T) {
				r := r
				wantOut := r.stdout
				if strings.HasPrefix(r.name, "gate deny") {
					// The verb's own deny, passed through byte-for-byte.
					r.out = denyOf(harness) + "\n"
					wantOut = line(denyOf)
				}
				home := t.TempDir()
				wrapperFakeSatelle(t, home, r)
				code, stdout, stderr := runHookScript(t, repo,
					renderHookCommand(repo, harness, r.sub), r.event(harness), home)
				if code != 0 {
					t.Fatalf("handler exit = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
				}
				if want := wantOut(harness); stdout != want {
					t.Fatalf("stdout = %q, want %q", stdout, want)
				}
			})
		}
	}

	// A commitgate deny the verb itself produced is passed through too.
	for _, harness := range []string{"claude", "grok", "pi", "cursor"} {
		home := t.TempDir()
		wrapperFakeSatelle(t, home, wrapperRow{fake: "y", out: denyOf(harness) + "\n", code: 1})
		_, stdout, _ := runHookScript(t, repo,
			renderHookCommand(repo, harness, "commitgate"), hookBashEvent(harness, "git commit -m x"), home)
		if stdout != denyOf(harness)+"\n" {
			t.Errorf("%s commitgate deny = %q", harness, stdout)
		}
	}

	// Cursor's infrastructure deny carries the reason where the model reads it.
	var doc map[string]any
	if err := json.Unmarshal([]byte(infraOf("cursor")), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["permission"] != "deny" || doc["user_message"] != preChangeInfraReason {
		t.Errorf("cursor infra deny = %v", doc)
	}
}
