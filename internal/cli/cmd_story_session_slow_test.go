//go:build clislow

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlowStorySessionReadsAndRebuildsFromAStoreFilledOnlyByPull(t *testing.T) {
	url, _, _ := attachmentServer(t)
	a := newCLIMachine(t, sessionToml)
	b := newCLIMachine(t, ledgerPushToml)

	a.use(t)
	repo := filepath.Dir(filepath.Dir(os.Getenv("SATELLE_CONFIG")))
	skill := filepath.Join(repo, "alt", "skills", "session-probe.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte(sessionSkillBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	id := createStoryID(t, "session probe")
	for _, actor := range []string{"reviewer", "executor"} {
		if out, err := runRoot(t, "ledger", "append", "--story", id, "--kind", "agent_invocation", "--actor", actor, "--body", "invoked "+actor); err != nil {
			t.Fatalf("ledger append: %v\n%s", err, out)
		}
	}
	runPush(t, url)

	// Machine B has nothing until it pulls.
	b.use(t)
	syncWorkstateCLI(t, url, "pull", id)

	var view struct {
		Configuration []struct {
			Bundle     string `json:"bundle"`
			BundleHeld bool   `json:"bundle_held"`
			Runs       int    `json:"runs"`
		} `json:"configuration"`
		Inputs struct {
			Story struct {
				Title string `json:"title"`
			} `json:"story"`
		} `json:"inputs"`
		Logs struct {
			Invocations []json.RawMessage `json:"invocations"`
		} `json:"logs"`
		Outputs struct {
			Status    string `json:"status"`
			Documents []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"documents"`
		} `json:"outputs"`
	}
	out, err := runRoot(t, "story", "session", id, "--json")
	if err != nil {
		t.Fatalf("story session: %v\n%s", err, out)
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("parse session: %v\n%s", err, out)
	}
	if len(view.Configuration) != 1 || view.Configuration[0].Runs != 2 || !view.Configuration[0].BundleHeld {
		t.Fatalf("configuration = %+v, want one pair used by both runs with its bundle held on B", view.Configuration)
	}
	if view.Inputs.Story.Title != "session probe" || len(view.Logs.Invocations) != 2 || view.Outputs.Status == "" {
		t.Errorf("session view lost the story's inputs, logs or status: %s", out)
	}
	var sawBundle bool
	for _, d := range view.Outputs.Documents {
		sawBundle = sawBundle || (d.Name == view.Configuration[0].Bundle && d.Type == "config-bundle")
	}
	if !sawBundle {
		t.Errorf("the bundle should be listed among the outputs: %s", out)
	}
	if plain, err := runRoot(t, "story", "session", id); err != nil || !strings.Contains(plain, "Configuration") || !strings.Contains(plain, "Outputs") {
		t.Errorf("plain session output: %v\n%s", err, plain)
	}

	// --config prints the content, digests recomputed equal to the recorded ones.
	bundle := view.Configuration[0].Bundle
	pair := strings.TrimPrefix(bundle, "config-")
	out, err = runRoot(t, "story", "session", id, "--config", pair, "--json")
	if err != nil {
		t.Fatalf("--config: %v\n%s", err, out)
	}
	var cfg sessionConfigJSON
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("parse --config: %v\n%s", err, out)
	}
	if !cfg.Verified || cfg.RecomputedSubstrate != cfg.SubstrateDigest || cfg.RecomputedBindings != cfg.BindingsDigest {
		t.Errorf("recomputed digests must equal the recorded ones: %+v", cfg)
	}
	var gotSkill bool
	for _, f := range cfg.Files {
		if f.Path == "skills/session-probe.md" {
			gotSkill = true
			if f.Content != sessionSkillBody {
				t.Errorf("skill body = %q, want it byte for byte (### heading intact)", f.Content)
			}
		}
		if strings.Contains(f.Path, "alt/") || strings.HasPrefix(f.Path, "/") {
			t.Errorf("path %q is not <kind>/<path under the kind's root>", f.Path)
		}
	}
	if !gotSkill {
		t.Errorf("the skill under the overridden root should appear as skills/session-probe.md: %+v", cfg.Files)
	}

	// --restore writes the same files, each through filebundle's path check.
	dir := filepath.Join(t.TempDir(), "restored")
	if out, err := runRoot(t, "story", "session", id, "--config", cfg.SubstrateDigest[:12], "--restore", dir); err != nil {
		t.Fatalf("--restore: %v\n%s", err, out)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "skills", "session-probe.md")); err != nil || string(got) != sessionSkillBody {
		t.Errorf("restored skill = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bindings.json")); err != nil {
		t.Errorf("bindings.json should be restored: %v", err)
	}

	// A bundle whose path climbs out is refused by --config and --restore alike,
	// and nothing is written.
	attachDoc(t, id, bundle, "config-bundle", `{"format":1,"files":[{"path":"../x","content":"a"}]}`)
	escape := filepath.Join(t.TempDir(), "escape")
	for _, args := range [][]string{
		{"story", "session", id, "--config", pair},
		{"story", "session", id, "--config", pair, "--restore", escape},
	} {
		if out, err := runRoot(t, args...); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Errorf("%v must refuse a `..` path, got err=%v\n%s", args, err, out)
		}
	}
	if _, err := os.Stat(escape); !os.IsNotExist(err) {
		t.Errorf("a refused restore must write nothing (err=%v)", err)
	}
}
