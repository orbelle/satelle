package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// sty_36d6f155 AC1: the claude adapter's shell env names the session, ahead of
// the pid lookup and behind SATELLE_SESSION.
func TestResolveSessionTakesTheClaudeEnvironmentID(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv("CURSOR_CONVERSATION_ID", "")
	t.Setenv(SessionEnv, "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "drv")
	PublishSession("published-elsewhere")
	if got := ResolveSession(); got != "drv" {
		t.Errorf("ResolveSession = %q, want the claude env id", got)
	}
}

// sty_36d6f155 AC1: a pid file written by another process that held this pid
// earlier, or by a build that recorded no identity, does not speak for this one.
func TestPublishedSessionIgnoresAFileForAReusedPid(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv("CURSOR_CONVERSATION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv(SessionEnv, "")
	if runtime.GOOS != "linux" {
		t.Skip("process identity is asserted on /proc")
	}
	dir := sessionPublishDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(dir, strconv.Itoa(os.Getpid()))
	if err := os.WriteFile(own, []byte("performer-id\tsome-earlier-process\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveSession(); got != "" {
		t.Errorf("a reused pid's file resolved %q, want none", got)
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Errorf("the mismatched file must be removed: %v", err)
	}

	if err := os.WriteFile(own, []byte("performer-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveSession(); got != "" {
		t.Errorf("a legacy file with no identity resolved %q, want none", got)
	}
}

func TestSessionLive(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	if SessionLive("sess-A") || SessionLive("") {
		t.Fatal("nothing published: no session is live")
	}
	PublishSession("sess-A")
	if !SessionLive("sess-A") {
		t.Error("a session this live process published must be live")
	}
	if SessionLive("sess-B") {
		t.Error("an id nothing publishes must not be live")
	}
	// A file for a pid nothing holds is stale: it neither counts nor stays.
	gone := filepath.Join(sessionPublishDir(), "2147483000")
	if err := os.WriteFile(gone, []byte("sess-gone\tx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if SessionLive("sess-gone") {
		t.Error("a dead process's session must not be live")
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("the stale file must be removed: %v", err)
	}
}

func TestPublishSessionResolvesWithoutEnv(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv("CURSOR_CONVERSATION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv(SessionEnv, "")
	_ = os.Unsetenv(SessionEnv)

	if got := ResolveSession(); got != "" {
		t.Fatalf("empty channel must resolve empty, got %q", got)
	}
	PublishSession("sess-A")
	if got := ResolveSession(); got != "sess-A" {
		t.Fatalf("published id must resolve without env, got %q", got)
	}
}

func TestResolveSessionPrefersEnvOverPublished(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	PublishSession("sess-published")
	t.Setenv(SessionEnv, "sess-env")
	if got := ResolveSession(); got != "sess-env" {
		t.Fatalf("env must win, got %q", got)
	}
}

func TestPublishSessionIgnoresEmpty(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	_ = os.Unsetenv(SessionEnv)
	PublishSession("  ")
	if got := ResolveSession(); got != "" {
		t.Fatalf("blank publish must not invent an id, got %q", got)
	}
	// File for this pid must not exist as a blank stamp.
	if _, err := os.Stat(sessionPublishDir() + "/" + strconv.Itoa(os.Getpid())); !os.IsNotExist(err) {
		t.Fatalf("blank publish must not write a file: %v", err)
	}
}

func TestPublishSessionModelRoundTrip(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	model, exe, reason := ResolveSessionModel("sess-A", "in-loop")
	if model != "" || exe != "" || reason != "" {
		t.Fatalf("unpublished pair must resolve empty, got (%q, %q, %q)", model, exe, reason)
	}
	PublishSessionModel("sess-A", "in-loop", "claude-opus-5-5", "claude", "")
	model, exe, reason = ResolveSessionModel("sess-A", "in-loop")
	if model != "claude-opus-5-5" || exe != "claude" || reason != "" {
		t.Fatalf("got (%q, %q, %q), want (claude-opus-5-5, claude, \"\")", model, exe, reason)
	}
	// A different role for the same session is a distinct slot.
	if model, _, _ := ResolveSessionModel("sess-A", "orchestrator"); model != "" {
		t.Fatalf("orchestrator role must not see the in-loop publish, got %q", model)
	}
}

func TestPublishSessionModelIgnoresEmpty(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	PublishSessionModel("", "in-loop", "opus", "claude", "")
	PublishSessionModel("sess-A", "", "opus", "claude", "")
	if model, _, _ := ResolveSessionModel("sess-A", "in-loop"); model != "" {
		t.Fatalf("empty sessionID/role must not publish, got %q", model)
	}
}

func TestPublishSessionModelReasonRoundTrip(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	PublishSessionModel("sess-B", "in-loop", "unknown", "grok", "grok's hook payload carries no model, so the in-loop tier is unknown")
	model, exe, reason := ResolveSessionModel("sess-B", "in-loop")
	if model != "unknown" || exe != "grok" || reason == "" {
		t.Fatalf("got (%q, %q, %q), want (unknown, grok, non-empty reason)", model, exe, reason)
	}
}

func TestResolveSessionModelReadsLegacyTwoFieldFile(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	dir := sessionModelDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Pre-change rows carried no third (reason) field.
	if err := os.WriteFile(dir+"/"+sessionModelFile("sess-legacy", "in-loop"), []byte("claude-opus-5-5\tclaude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model, exe, reason := ResolveSessionModel("sess-legacy", "in-loop")
	if model != "claude-opus-5-5" || exe != "claude" || reason != "" {
		t.Fatalf("got (%q, %q, %q), want (claude-opus-5-5, claude, \"\")", model, exe, reason)
	}
}
