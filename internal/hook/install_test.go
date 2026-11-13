package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"
)

// SatelleCall reads only the first whitespace-separated word: a satelle
// binary by basename is a call, any wrapper is not; ownsCommand is a call
// whose first word after satelle is hook, or the legacy script.
func TestSatelleCall(t *testing.T) {
	for _, c := range []struct {
		cmd   string
		words []string
	}{
		{"satelle reindex", []string{"reindex"}},
		{"/usr/local/bin/satelle hook gate --harness claude", []string{"hook", "gate"}},
		{"satelle doctor || true", []string{"doctor", "||", "true"}},
		{"satelle --version", nil},
		{"SATELLE.EXE reindex", []string{"reindex"}},
	} {
		words, ok := SatelleCall(c.cmd)
		if !ok || !slices.Equal(words, c.words) {
			t.Errorf("SatelleCall(%q) = %q, %v; want %q, true", c.cmd, words, ok, c.words)
		}
	}
	for _, cmd := range []string{
		"FOO=1 satelle reindex",
		"env X=1 satelle reindex",
		`sh -c "satelle reindex"`,
		"timeout 5 satelle reindex",
		"if true; then satelle reindex; fi",
		"cd x && satelle reindex",
		"echo satelle is great",
		"npm test",
		"",
	} {
		if _, ok := SatelleCall(cmd); ok {
			t.Errorf("SatelleCall(%q) is a satelle call; want not", cmd)
		}
	}
	for cmd, want := range map[string]bool{
		"satelle hook gate --harness claude":                true,
		"/usr/local/bin/satelle hook gate --harness claude": true,
		".satelle/hooks/satelle-hook.sh pre":                true,
		`echo "satelle hook gate"`:                          false,
		"timeout 5 satelle hook gate --harness claude":      false,
		"satelle doctor":                                    false,
	} {
		if got := ownsCommand(cmd); got != want {
			t.Errorf("ownsCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// handlers lists every handler of doc's hooks as {event, matcher, command}
// maps (the handler itself for a flat entry), so a test can compare them.
func handlers(doc map[string]any) []string {
	var out []string
	hooks, _ := doc["hooks"].(map[string]any)
	for event, v := range hooks {
		for _, g := range v.([]any) {
			gm := g.(map[string]any)
			if hs, nested := gm["hooks"].([]any); nested {
				for _, h := range hs {
					b, _ := json.Marshal(map[string]any{"event": event, "matcher": gm["matcher"], "handler": h})
					out = append(out, string(b))
				}
				continue
			}
			b, _ := json.Marshal(map[string]any{"event": event, "handler": gm})
			out = append(out, string(b))
		}
	}
	sort.Strings(out)
	return out
}

func lacksReindex(cmd string) bool {
	words, ok := SatelleCall(cmd)
	return ok && len(words) > 0 && words[0] == "reindex"
}

// Refresh adds or updates satelle's entries: its own hooks become the current
// set once each, a first-word satelle command the predicate calls stale goes,
// and every other handler is kept as it was.
func TestRefreshAddsAndUpdatesSatelleEntries(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, ".claude", "settings.json")
	writeFile(t, claude, `{
  "permissions": {"allow": ["Bash(ls)"]},
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "satelle reindex"}]},
      {"hooks": [{"type": "command", "command": "echo hi"}]}
    ],
    "PreToolUse": [
      {"matcher": "Edit|Write", "hooks": [{"type": "command", "command": "satelle hook gate --harness claude"}]},
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "npm test"}]}
    ],
    "Stop": [
      {"hooks": [
        {"type": "command", "command": "satelle doctor"},
        {"type": "command", "command": "echo satelle is great"},
        {"type": "command", "command": "sh -c \"satelle reindex\""},
        {"type": "command", "command": "FOO=1 satelle reindex"}
      ]}
    ]
  }
}
`)
	cursor := filepath.Join(root, ".cursor", "hooks.json")
	writeFile(t, cursor, `{"version": 1, "hooks": {"stop": [{"command": "satelle reindex"}, {"command": "echo satelle is great"}]}}`)

	kept := []string{"echo hi", "npm test", "satelle doctor", "echo satelle is great", `sh -c "satelle reindex"`, "FOO=1 satelle reindex"}
	for _, h := range []string{Claude, Cursor, Grok} {
		path, changed, err := Refresh(root, h, lacksReindex)
		if err != nil || !changed {
			t.Fatalf("Refresh(%s) = %v, %v; want changed", h, changed, err)
		}
		cmds, err := Commands(root, h)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(cmds, "satelle reindex") {
			t.Errorf("%s still runs satelle reindex: %q", path, cmds)
		}
		for _, verb := range []string{VerbGate, VerbContext, VerbPrompt, VerbStopcheck} {
			n := 0
			for _, c := range cmds {
				if c == Command(verb, h) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s holds %q %d times, want once", path, Command(verb, h), n)
			}
		}
		switch h {
		case Claude:
			for _, k := range kept {
				if !slices.Contains(cmds, k) {
					t.Errorf("%s lost %q", path, k)
				}
			}
			// The kept handlers are deep-equal to what they were.
			orig := handlers(map[string]any{"hooks": map[string]any{
				"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo hi"}}}},
				"PreToolUse":   []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "npm test"}}}},
				"Stop": []any{map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "satelle doctor"},
					map[string]any{"type": "command", "command": "echo satelle is great"},
					map[string]any{"type": "command", "command": `sh -c "satelle reindex"`},
					map[string]any{"type": "command", "command": "FOO=1 satelle reindex"},
				}}},
			}})
			got := handlers(readDoc(t, path))
			for _, o := range orig {
				if !slices.Contains(got, o) {
					t.Errorf("%s: handler %s not kept as it was", path, o)
				}
			}
			if doc := readDoc(t, path); !reflect.DeepEqual(doc["permissions"], map[string]any{"allow": []any{"Bash(ls)"}}) {
				t.Errorf("%s: permissions changed: %v", path, doc["permissions"])
			}
		case Cursor:
			if !slices.Contains(cmds, "echo satelle is great") {
				t.Errorf("%s lost the flat echo entry", path)
			}
		case Grok:
			if _, err := os.Stat(path); err != nil {
				t.Errorf("grok file not created: %v", err)
			}
		}
	}
}

// A file whose resulting hooks equal its current hooks is not written, whatever
// its indentation or key order: bytes and mtime stay as they were.
func TestInstallWritesNothingWhenHooksAreEqual(t *testing.T) {
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, h := range []string{Claude, Grok, Cursor} {
		t.Run(h, func(t *testing.T) {
			root := t.TempDir()
			path, changed, err := Install(root, h, nil)
			if err != nil || !changed {
				t.Fatalf("first Install = %v, %v", changed, err)
			}
			// Re-encode: four-space indent, top-level keys in reverse order.
			doc := readDoc(t, path)
			keys := make([]string, 0, len(doc))
			for k := range doc {
				keys = append(keys, k)
			}
			sort.Sort(sort.Reverse(sort.StringSlice(keys)))
			var buf bytes.Buffer
			buf.WriteString("{\n")
			for i, k := range keys {
				v, _ := json.MarshalIndent(doc[k], "    ", "    ")
				kb, _ := json.Marshal(k)
				buf.WriteString("    " + string(kb) + ": " + string(v))
				if i < len(keys)-1 {
					buf.WriteString(",")
				}
				buf.WriteString("\n")
			}
			buf.WriteString("}\n")
			want := buf.Bytes()
			writeFile(t, path, string(want))
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			for name, f := range map[string]func(string, string, func(string) bool) (string, bool, error){
				"Install": Install, "Refresh": Refresh, "Pending": Pending,
			} {
				_, changed, err := f(root, h, lacksReindex)
				if err != nil || changed {
					t.Errorf("%s = changed %v, %v; want unchanged", name, changed, err)
				}
				got, _ := os.ReadFile(path)
				st, _ := os.Stat(path)
				if !bytes.Equal(got, want) || !st.ModTime().Equal(old) {
					t.Errorf("%s rewrote %s", name, path)
				}
			}
		})
	}
	t.Run(Pi, func(t *testing.T) {
		root := t.TempDir()
		path, _, err := Install(root, Pi, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := Install(root, Pi, nil); err != nil || changed {
			t.Errorf("second Install = %v, %v; want unchanged", changed, err)
		}
		if st, _ := os.Stat(path); !st.ModTime().Equal(old) {
			t.Error("second Install rewrote the pi extension")
		}
	})
}

// Refresh and Pending leave a pi extension satelle does not own; a hand-run
// Install replaces it; a marked file with old content is refreshed.
func TestRefreshLeavesAnUnownedPiExtension(t *testing.T) {
	root := t.TempDir()
	rel, _ := File(Pi)
	path := filepath.Join(root, filepath.FromSlash(rel))
	writeFile(t, path, "// mine\n")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func(string, string, func(string) bool) (string, bool, error){"Pending": Pending, "Refresh": Refresh} {
		if _, _, err := f(root, Pi, nil); !errors.Is(err, ErrNotOwned) {
			t.Errorf("%s = %v, want ErrNotOwned", name, err)
		}
		b, _ := os.ReadFile(path)
		st, _ := os.Stat(path)
		if string(b) != "// mine\n" || !st.ModTime().Equal(old) {
			t.Errorf("%s touched the unowned extension", name)
		}
	}
	if _, changed, err := Install(root, Pi, nil); err != nil || !changed {
		t.Fatalf("Install = %v, %v; want it replaced", changed, err)
	}
	writeFile(t, path, piOwnedMarker+" old\n")
	if _, changed, err := Refresh(root, Pi, nil); err != nil || !changed {
		t.Fatalf("Refresh of a marked old file = %v, %v; want rewritten", changed, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, piExtension()) {
		t.Error("Refresh did not write the current extension")
	}
}

// Commands lists nested and flat handlers; pi has none.
func TestCommandsListsEveryHandler(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".claude", "settings.json"),
		`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"a"},{"type":"command","command":"b"}]}]}}`)
	writeFile(t, filepath.Join(root, ".cursor", "hooks.json"), `{"version":1,"hooks":{"stop":[{"command":"c"}]}}`)
	got, err := Commands(root, Claude)
	sort.Strings(got)
	if err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("claude Commands = %q, %v", got, err)
	}
	if got, err := Commands(root, Cursor); err != nil || !slices.Equal(got, []string{"c"}) {
		t.Errorf("cursor Commands = %q, %v", got, err)
	}
	if got, err := Commands(root, Grok); err != nil || got != nil {
		t.Errorf("absent grok Commands = %q, %v", got, err)
	}
	if _, _, err := Install(root, Pi, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := Commands(root, Pi); err != nil || got != nil {
		t.Errorf("pi Commands = %q, %v", got, err)
	}
}

// ForCLI maps each harness's command-line program to the harness, and nothing
// else: the registry, not the CLI layer, owns which programs are harnesses.
func TestForCLI(t *testing.T) {
	for cli, want := range map[string]string{"claude": Claude, "pi": Pi, "grok": Grok, "cursor-agent": Cursor} {
		if got, ok := ForCLI(cli); !ok || got != want {
			t.Errorf("ForCLI(%q) = %q, %v; want %q", cli, got, ok, want)
		}
	}
	for _, cli := range []string{"session", "cursor", "codex", ""} {
		if h, ok := ForCLI(cli); ok {
			t.Errorf("ForCLI(%q) = %q; want no harness", cli, h)
		}
	}
}
