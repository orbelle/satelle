package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookContextNamesTheSessionWhenTheConfigurationIsInvalid(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	settings := filepath.Join(dir, ".satelle", "satelle.toml")
	good, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, append(append([]byte{}, good...), "\n[mystery]\nx = 1\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "", "validate"); err == nil {
		t.Fatal("the fixture's configuration is valid")
	}
	invalid := "configuration is invalid; run `satelle validate`"
	withSession := fmt.Sprintf(`{"session_id":"s-bad","cwd":%q}`, dir)

	t.Run("session id", func(t *testing.T) {
		for _, ev := range []string{"context", "prompt"} {
			raw, err := run(t, withSession, "hook", ev, "--harness", "claude")
			if err != nil {
				t.Fatalf("hook %s: %v", ev, err)
			}
			out := hookText(raw)
			hasAll(t, "hook "+ev, out, "satelle session: s-bad", invalid)
			hasNone(t, "hook "+ev, out, "satelle session: unknown")
			if i, s := strings.Index(out, invalid), strings.Index(out, "satelle session: s-bad"); i < 0 || s < 0 || i > s {
				t.Errorf("hook %s: the invalid line is not before the session line:\n%s", ev, out)
			}
		}
	})

	t.Run("no session id", func(t *testing.T) {
		raw, err := run(t, fmt.Sprintf(`{"cwd":%q}`, dir), "hook", "context", "--harness", "claude")
		if err != nil {
			t.Fatal(err)
		}
		hasAll(t, "hook context", hookText(raw),
			"satelle session: unknown (the harness sent no session id and SATELLE_SESSION is unset).", invalid)
	})

	t.Run("repaired", func(t *testing.T) {
		if err := os.WriteFile(settings, good, 0o644); err != nil {
			t.Fatal(err)
		}
		id := createID(t, "story", "create", "--title", "seated")
		must(t, "story", "seat", id, "--session", "s-bad")
		raw, err := run(t, withSession, "hook", "context", "--harness", "claude")
		if err != nil {
			t.Fatal(err)
		}
		out := hookText(raw)
		hasAll(t, "hook context", out, "satelle session: s-bad", id)
		hasNone(t, "hook context", out, "configuration is invalid")
	})
}
