package agentstep

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/verb"
)

// A value satelle placed in its own process environment (a remote node's
// run-minted github-app token) is scrubbed from a performer's output with the
// binding's own env, and clearing the set stops scrubbing it.
func TestProcessSecretsAreScrubbedFromPerformerOutput(t *testing.T) {
	const token = "ghs_PROCESS-SECRET-VALUE"
	g := New(nil, nil, t.TempDir(), "")
	g.SetProcessSecrets(map[string]string{"GH_TOKEN": token})

	out := "pushed with " + token + " and key-from-binding\n"
	got := verb.OutputTail(out, g.scrubValues(map[string]string{"API_KEY": "key-from-binding"}))
	if strings.Contains(got, token) || strings.Contains(got, "key-from-binding") {
		t.Errorf("OutputTail = %q, want both the process secret and the binding value scrubbed", got)
	}

	g.SetProcessSecrets(nil)
	if _, ok := g.scrubValues(nil)["GH_TOKEN"]; ok {
		t.Error("a cleared process secret is still scrubbed")
	}
}

// The set a dispatch's output is redacted of at its source (agentcli.Request.Redact,
// built as scrubValues(nil, resolved)) carries the process secrets beside what the
// dispatch resolved, so the bytes every log, row and result is made from never
// hold the value, and without process secrets it is just what was resolved.
func TestProcessSecretsJoinTheSourceRedactionSet(t *testing.T) {
	const token = "ghs_PROCESS-SECRET-VALUE"
	resolved := map[string]string{"infisical://p/dev/API_KEY": "resolved-api-key-value"}
	g := New(nil, nil, t.TempDir(), "")
	g.SetProcessSecrets(map[string]string{"GH_TOKEN": token})

	out := string(redactBytes([]byte("used "+token+" and resolved-api-key-value\n"), g.scrubValues(nil, resolved)))
	if strings.Contains(out, token) || strings.Contains(out, "resolved-api-key-value") ||
		!strings.Contains(out, "[redacted:GH_TOKEN]") {
		t.Errorf("source-redacted output = %q, want the process secret and the resolved value redacted", out)
	}

	g.SetProcessSecrets(nil)
	if got := g.scrubValues(nil, resolved); len(got) != 1 || got["infisical://p/dev/API_KEY"] != "resolved-api-key-value" {
		t.Errorf("redaction set without process secrets = %v, want only the resolved set", got)
	}
}
