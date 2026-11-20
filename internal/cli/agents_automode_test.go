package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/substrate"
)

func TestAgentsAutoModeClaudePrintsWithoutWriting(t *testing.T) {
	dir := isolate(t)
	must(t, "init")

	custom := "\nCustom repo guidance line one.\r\n\n  Run commands with --session <session> && verify > log  \n\nCustom line three.\n"
	autoModePath := filepath.Join(dir, ".satelle", kernel.AutoModeFile)
	if err := os.WriteFile(autoModePath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	home := os.Getenv("HOME")
	userClaude := filepath.Join(home, ".claude", "settings.json")
	repoClaude := filepath.Join(dir, ".claude", "settings.json")

	checkOutput := func(args ...string) {
		t.Helper()
		stdout, stderr, err := runSplit(t, "", args...)
		if err != nil {
			t.Fatalf("satelle %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		if strings.Contains(stdout, `\u003c`) || strings.Contains(stdout, `\u0026`) {
			t.Errorf("stdout HTML-escaped prose:\n%s", stdout)
		}
		if !strings.Contains(stdout, "<session> && verify > log") {
			t.Errorf("stdout did not preserve raw characters:\n%s", stdout)
		}
		var top map[string]any
		if err := json.Unmarshal([]byte(stdout), &top); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
		}
		if len(top) != 1 {
			t.Fatalf("stdout top-level keys = %v, want only autoMode", top)
		}
		am, ok := top["autoMode"].(map[string]any)
		if !ok || len(am) != 1 {
			t.Fatalf("autoMode = %#v, want object with only environment", top["autoMode"])
		}
		var doc struct {
			AutoMode struct {
				Environment []string `json:"environment"`
			} `json:"autoMode"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("decode autoMode: %v", err)
		}
		want := []string{
			"$defaults",
			"Custom repo guidance line one.",
			"Run commands with --session <session> && verify > log",
			"Custom line three.",
		}
		if !slices.Equal(doc.AutoMode.Environment, want) {
			t.Errorf("environment = %#v, want %#v", doc.AutoMode.Environment, want)
		}
		if !strings.Contains(stderr, "~/.claude/settings.json") {
			t.Errorf("stderr = %q, want ~/.claude/settings.json named", stderr)
		}
	}

	checkOutput("agents", "auto-mode", "claude")
	checkOutput("agents", "auto-mode", "claude", "--session", "s-1")

	if _, err := os.Stat(userClaude); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist, got err %v", userClaude, err)
	}
	if _, err := os.Stat(repoClaude); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist, got err %v", repoClaude, err)
	}

	if err := os.MkdirAll(filepath.Dir(userClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	const existingSettings = "{\"theme\":\"dark\"}\n"
	if err := os.WriteFile(userClaude, []byte(existingSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	fixedTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(userClaude, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	checkOutput("agents", "auto-mode", "claude")
	afterBytes, err := os.ReadFile(userClaude)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBytes) != existingSettings {
		t.Errorf("pre-existing %s content changed: %q", userClaude, string(afterBytes))
	}
	afterInfo, err := os.Stat(userClaude)
	if err != nil {
		t.Fatal(err)
	}
	if !afterInfo.ModTime().Equal(fixedTime) {
		t.Errorf("pre-existing %s mtime changed: got %v, want %v", userClaude, afterInfo.ModTime(), fixedTime)
	}
	afterGuidance, err := os.ReadFile(autoModePath)
	if err != nil || string(afterGuidance) != custom {
		t.Errorf(".satelle/%s changed (%v): %q", kernel.AutoModeFile, err, string(afterGuidance))
	}

	t.Run("unknown write flag refused", func(t *testing.T) {
		stdout, stderr, err := runSplit(t, "", "agents", "auto-mode", "claude", "--write")
		if err == nil {
			t.Fatalf("--write succeeded:\nstdout:%s\nstderr:%s", stdout, stderr)
		}
		if ExitCodeOf(err) != ExitError {
			t.Errorf("exit code = %d, want %d", ExitCodeOf(err), ExitError)
		}
		if !strings.Contains(err.Error(), "unknown flag") || !strings.Contains(err.Error(), "--write") {
			t.Errorf("error = %q, want unknown flag --write", err.Error())
		}
		if stdout != "" {
			t.Errorf("stdout not empty on unknown flag: %q", stdout)
		}
	})

	t.Run("missing guidance file refused", func(t *testing.T) {
		if err := os.Remove(autoModePath); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(autoModePath, []byte(custom), 0o644) })
		stdout, _, err := runSplit(t, "", "agents", "auto-mode", "claude")
		if err == nil {
			t.Fatalf("missing %s succeeded:\n%s", kernel.AutoModeFile, stdout)
		}
		if ExitCodeOf(err) != ExitError {
			t.Errorf("exit code = %d, want %d", ExitCodeOf(err), ExitError)
		}
		if stdout != "" {
			t.Errorf("stdout not empty on missing file: %q", stdout)
		}
		msg := err.Error()
		if !strings.Contains(msg, ".satelle/auto-mode.md") || !strings.Contains(msg, "satelle init --print") {
			t.Errorf("error = %q, want .satelle/auto-mode.md and satelle init --print", msg)
		}
	})

	t.Run("symlink guidance file refused", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks require elevated privileges on windows")
		}
		secret := filepath.Join(t.TempDir(), "secret.txt")
		const secretBody = "SUPER_SECRET_OUTSIDE_REPO_TOKEN"
		if err := os.WriteFile(secret, []byte(secretBody+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(autoModePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, autoModePath); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.Remove(autoModePath)
			_ = os.WriteFile(autoModePath, []byte(custom), 0o644)
		})
		stdout, stderr, err := runSplit(t, "", "agents", "auto-mode", "claude")
		if err == nil {
			t.Fatalf("symlinked %s succeeded:\n%s", kernel.AutoModeFile, stdout)
		}
		if ExitCodeOf(err) != ExitError {
			t.Errorf("exit code = %d, want %d", ExitCodeOf(err), ExitError)
		}
		if stdout != "" {
			t.Errorf("stdout not empty on symlink refusal: %q", stdout)
		}
		if strings.Contains(stdout+stderr+err.Error(), secretBody) {
			t.Errorf("symlink target contents leaked")
		}
		if !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("error = %q, want not a regular file", err.Error())
		}
	})

	t.Run("directory guidance path refused", func(t *testing.T) {
		if err := os.Remove(autoModePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(autoModePath, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.RemoveAll(autoModePath)
			_ = os.WriteFile(autoModePath, []byte(custom), 0o644)
		})
		stdout, _, err := runSplit(t, "", "agents", "auto-mode", "claude")
		if err == nil {
			t.Fatalf("directory %s succeeded:\n%s", kernel.AutoModeFile, stdout)
		}
		if stdout != "" || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("stdout=%q err=%v, want empty stdout and not a regular file", stdout, err)
		}
	})

	t.Run("non-repo directory refused", func(t *testing.T) {
		empty := t.TempDir()
		stdout, _, err := runSplit(t, "", "-C", empty, "agents", "auto-mode", "claude")
		if err == nil {
			t.Fatalf("non-repo succeeded:\n%s", stdout)
		}
		if ExitCodeOf(err) != ExitError {
			t.Errorf("exit code = %d, want %d", ExitCodeOf(err), ExitError)
		}
		if stdout != "" {
			t.Errorf("stdout not empty outside repo: %q", stdout)
		}
	})
}

func TestAgentsAutoModeOtherHarnessesUnavailable(t *testing.T) {
	isolate(t)
	must(t, "init")

	for _, h := range []string{hook.Pi, hook.Grok, hook.Cursor} {
		stdout, stderr, err := runSplit(t, "", "agents", "auto-mode", h)
		if err == nil {
			t.Fatalf("agents auto-mode %s succeeded:\n%s%s", h, stdout, stderr)
		}
		if ExitCodeOf(err) != ExitError {
			t.Errorf("%s: exit code = %d, want %d", h, ExitCodeOf(err), ExitError)
		}
		if stdout != "" || stderr != "" {
			t.Errorf("%s: stdout=%q stderr=%q, want both empty", h, stdout, stderr)
		}
		if !strings.Contains(err.Error(), h) || !strings.Contains(err.Error(), "unavailable") {
			t.Errorf("%s: error = %q, want harness name and unavailable", h, err.Error())
		}
	}

	stdout, _, err := runSplit(t, "", "agents", "auto-mode", "bogus")
	if err == nil || ExitCodeOf(err) != ExitError || stdout != "" {
		t.Fatalf("agents auto-mode bogus: stdout=%q err=%v", stdout, err)
	}
	if !strings.Contains(err.Error(), `unknown harness "bogus"`) {
		t.Errorf("bogus error = %q, want unknown harness", err.Error())
	}

	empty := t.TempDir()
	_, _, err = runSplit(t, "", "-C", empty, "agents", "auto-mode", hook.Cursor)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("unsupported harness outside repo: err = %v, want unavailable before repo lookup", err)
	}
}

func TestInitWritesAutoModeGuidance(t *testing.T) {
	dir := isolate(t)
	must(t, "init")

	shipped, err := kernel.Shipped(kernel.AutoModeFile)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, ".satelle", kernel.AutoModeFile))
	if err != nil {
		t.Fatalf("init did not write .satelle/%s: %v", kernel.AutoModeFile, err)
	}
	if string(written) != string(shipped) {
		t.Fatalf("init .satelle/%s differs from template:\ngot:\n%s\nwant:\n%s", kernel.AutoModeFile, written, shipped)
	}

	printed := must(t, "init", "--print", ".satelle/"+kernel.AutoModeFile)
	if printed != string(shipped) {
		t.Errorf("init --print .satelle/%s differs from template", kernel.AutoModeFile)
	}

	valOut := must(t, "validate", "--json")
	var valDoc struct {
		Valid    bool                `json:"valid"`
		Volatile []substrate.Finding `json:"volatile"`
	}
	if err := json.Unmarshal([]byte(valOut), &valDoc); err != nil {
		t.Fatalf("validate --json: %v\n%s", err, valOut)
	}
	if !valDoc.Valid || len(valDoc.Volatile) != 0 {
		t.Errorf("validate --json: valid=%v volatile=%v", valDoc.Valid, valDoc.Volatile)
	}

	stdout, stderr, err := runSplit(t, "", "agents", "auto-mode", "claude")
	if err != nil {
		t.Fatalf("agents auto-mode claude: %v\n%s%s", err, stdout, stderr)
	}
	var doc struct {
		AutoMode struct {
			Environment []string `json:"environment"`
		} `json:"autoMode"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode autoMode: %v\n%s", err, stdout)
	}
	want := append([]string{"$defaults"}, hook.AutoModeLines(shipped)...)
	if !slices.Equal(doc.AutoMode.Environment, want) {
		t.Errorf("environment = %#v, want %#v", doc.AutoMode.Environment, want)
	}
	if !strings.Contains(stderr, "~/.claude/settings.json") {
		t.Errorf("stderr = %q, want ~/.claude/settings.json", stderr)
	}
}
