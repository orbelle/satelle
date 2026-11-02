package agentcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failingStub writes a CLI stand-in that runs body then exits 1.
func failingStub(t *testing.T, body string) Runner {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\n"+body+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := RunnerFromCommand(bin + " -p")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// sty_4bf6adbe AC1: claude -p --output-format json reports its failure as a JSON
// result on stdout with stderr empty; the error carries that result.
func TestFailedRunErrorCarriesStdoutJSONError(t *testing.T) {
	r := failingStub(t, `echo '{"type":"result","is_error":true,"result":"Invalid API key"}'`)
	_, err := r.Run(context.Background(), Request{SystemPrompt: "S", Payload: "P"})
	if err == nil {
		t.Fatal("exit 1 must be an error")
	}
	if !strings.Contains(err.Error(), "Invalid API key") || !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("error must carry the CLI's own error and the exit status: %v", err)
	}
}

// sty_4bf6adbe AC1: an error field stands in when there is no result; plain stdout
// stands in when there is no JSON error; stderr is appended; each part is capped.
func TestFailedRunErrorFallbacksAndCap(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"error field", `echo '{"error":{"message":"model not found"}}'`, []string{"model not found"}},
		{"stdout tail", `echo 'plain failure text'`, []string{"plain failure text"}},
		{"stdout and stderr", `echo 'out text'; echo 'err text' >&2`, []string{"out text", "stderr: err text"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := failingStub(t, c.body).Run(context.Background(), Request{Payload: "P"})
			if err == nil {
				t.Fatal("exit 1 must be an error")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}

	long := "HEAD" + strings.Repeat("x", 5000) + "END"
	detail := failureDetail([]byte(long), long, nil)
	if !strings.Contains(detail, "HEAD") || !strings.Contains(detail, "END") || len(detail) > 2*failureDetailCap+64 {
		t.Errorf("each part must be clipped to %d bytes, head and tail kept: len %d", failureDetailCap, len(detail))
	}
}

// sty_4bf6adbe AC3: a secret the CLI echoes to stdout and stderr never reaches the
// error.
func TestFailedRunErrorRedactsSecrets(t *testing.T) {
	const secret = "s3cr3t-value-0123456789"
	r := failingStub(t, `echo "{\"is_error\":true,\"result\":\"bad key $SECRET_TOKEN\"}"; echo "stderr has $SECRET_TOKEN" >&2`)
	_, err := r.Run(context.Background(), Request{
		Payload: "P",
		Env:     map[string]string{"SECRET_TOKEN": secret},
		Redact:  map[string]string{"SECRET_TOKEN": secret},
	})
	if err == nil {
		t.Fatal("exit 1 must be an error")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("secret must be redacted: %v", err)
	}
}
