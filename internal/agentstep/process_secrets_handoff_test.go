package agentstep

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

const handoffKey = "SATELLE_TEST_HANDOFF_TOKEN"

// unsetForTest removes key from the process environment for the test.
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	_ = os.Unsetenv(key)
}

// An engine starts with the secrets a running node join handed off as its
// process secrets.
func TestNewSeedsProcessSecretsFromTheHandoff(t *testing.T) {
	want := map[string]string{handoffKey: "handed-off"}
	prev := loadProcessSecrets
	loadProcessSecrets = func() map[string]string { return want }
	t.Cleanup(func() { loadProcessSecrets = prev })

	g := New(nil, fakeDocs{workflow: testWorkflow}, "/repo", "")
	if !reflect.DeepEqual(g.processSecrets, want) {
		t.Errorf("processSecrets = %v, want %v", g.processSecrets, want)
	}

	loadProcessSecrets = func() map[string]string { return nil }
	if g := New(nil, fakeDocs{workflow: testWorkflow}, "/repo", ""); g.processSecrets != nil {
		t.Errorf("with no hand-off processSecrets = %v, want none", g.processSecrets)
	}
}

// invokeReviewerEnv dispatches one reviewer through Invoke under binding env
// and returns the environment overlay the runner received.
func invokeReviewerEnv(t *testing.T, secrets, bindingEnv map[string]string) map[string]string {
	t.Helper()
	r := &fakeRunner{out: `{"decision":"accept","notes":"ok"}`}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	g.SetProcessSecrets(secrets)
	b := config.AgentBinding{Command: "claude", Tools: "Read", Principles: config.PrinciplesNone, Env: bindingEnv}
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: b, Section: "reviewer", Rubric: "judge",
		Payload: map[string]string{}, Expect: ExpectVerdict, Runner: r, Skill: "s",
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	return r.got.Env
}

// A process secret the process's own environment lacks reaches the dispatched
// child's environment; a key the binding sets keeps the binding's value, and a
// key the environment already carries is left to inheritance.
func TestInvokeHandsAMissingProcessSecretToTheChild(t *testing.T) {
	unsetForTest(t, handoffKey)
	secrets := map[string]string{handoffKey: "handed-off"}

	if got := invokeReviewerEnv(t, secrets, nil)[handoffKey]; got != "handed-off" {
		t.Errorf("dispatched env %s = %q, want the handed-off value", handoffKey, got)
	}
	if got := invokeReviewerEnv(t, secrets, map[string]string{handoffKey: "from-binding"})[handoffKey]; got != "from-binding" {
		t.Errorf("dispatched env %s = %q, want the binding's value", handoffKey, got)
	}

	t.Setenv(handoffKey, "inherited")
	if got, ok := invokeReviewerEnv(t, secrets, nil)[handoffKey]; ok {
		t.Errorf("dispatched env overlays %s = %q over the inherited value, want it left to inheritance", handoffKey, got)
	}
}
