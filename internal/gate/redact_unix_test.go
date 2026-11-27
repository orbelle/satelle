//go:build !windows

package gate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func secretValue(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)[:n]
}

func redactorOf(v string) func(string) string {
	return func(s string) string { return strings.ReplaceAll(s, v, "[secret]") }
}

func noWindowOf(t *testing.T, where, s, v string) {
	t.Helper()
	if !strings.Contains(s, "[secret]") {
		t.Errorf("%s holds no [secret]", where)
	}
	for i := 0; i+16 <= len(v); i++ {
		if strings.Contains(s, v[i:i+16]) {
			t.Errorf("%s holds a 16-byte window of the value at %d", where, i)
			return
		}
	}
}

func TestRunCheckCodeRedactsBeforeBound(t *testing.T) {
	const script = `printf '%s' "$PRE$SATELLE_TEST_SECRET$POST"`
	for _, tc := range []struct {
		name      string
		pre, post int
		value     int
	}{
		{"straddling byte 2048", 1900, 4000, 300},
		{"straddling len-2048", 4000, 1900, 300},
		{"longer than 4096", 100, 100, 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := secretValue(t, tc.value)
			t.Setenv("SATELLE_TEST_SECRET", v)
			t.Setenv("PRE", strings.Repeat("a", tc.pre))
			t.Setenv("POST", strings.Repeat("b", tc.post))
			_, notes, err := RunCheckCode(context.Background(), script, t.TempDir(), CheckEnv{Redact: redactorOf(v)}, 0)
			if err != nil {
				t.Fatal(err)
			}
			if tc.pre+tc.post > 2*outputBound && !strings.Contains(notes, "bytes elided") {
				t.Fatalf("the output was not bounded: %d bytes", len(notes))
			}
			noWindowOf(t, "the notes", notes, v)
		})
	}
	t.Run("no redactor", func(t *testing.T) {
		v := secretValue(t, 300)
		t.Setenv("SATELLE_TEST_SECRET", v)
		t.Setenv("PRE", strings.Repeat("a", 1900))
		t.Setenv("POST", strings.Repeat("b", 4000))
		_, notes, err := RunCheckCode(context.Background(), script, t.TempDir(), CheckEnv{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(notes, "bytes elided") || len(notes) > 2*outputBound+64 || strings.Contains(notes, "[secret]") {
			t.Fatalf("bound changed with no redactor: %d bytes", len(notes))
		}
	})
}

func TestJudgeTextRedactedBeforeTheSnippet(t *testing.T) {
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"g"}}}, map[string]workflow.Gate{"g": {Skill: "judge", Agent: "y1"}})
	v := secretValue(t, 200)
	d.answers["y1"] = strings.Repeat("a", 1000) + v + strings.Repeat("b", 400)
	r.Redact = redactorOf(v)
	rs, _ := r.Execute(context.Background())
	if len(rs) != 1 {
		t.Fatalf("results = %+v", rs)
	}
	noWindowOf(t, "the notes", rs[0].Notes, v)
	for i := 0; i+16 <= len(v); i++ {
		if strings.Contains(rs[0].Answer, v[i:i+16]) {
			t.Fatalf("the answer holds a 16-byte window of the value at %d", i)
		}
	}
}

func TestFallbackErrorRedacted(t *testing.T) {
	v := secretValue(t, 200)
	r, d := fallbackRun(workflow.Gate{Skill: "judge", Agent: "dead"}, map[string]string{"dead": "y1"})
	r.Redact = redactorOf(v)
	d.errs = map[string]error{"dead": errors.New("harness exited: token " + v)}
	d.redacted = map[string]bool{}
	rs, _ := r.Execute(context.Background())
	if rs[0].FallbackFrom == nil {
		t.Fatalf("no fallback: %+v", rs[0])
	}
	noWindowOf(t, "fallback_from.error", rs[0].FallbackFrom.Error, v)
	noWindowOf(t, "the notes", rs[0].Notes, v)
	raw, err := json.Marshal(rs[0])
	if err != nil {
		t.Fatal(err)
	}
	noWindowOf(t, "the gate.run record", string(raw), v)
	if !d.redacted["dead"] || !d.redacted["y1"] {
		t.Fatalf("a judge request carried no redactor: %v", d.redacted)
	}
	r, d = fallbackRun(workflow.Gate{Skill: "judge", Panel: []string{"dead", "n1"}, Combine: workflow.CombineAll}, map[string]string{"dead": "y2"})
	r.Redact = redactorOf(v)
	d.errs = map[string]error{"dead": errors.New("harness exited: token " + v)}
	rs, _ = r.Execute(context.Background())
	fell := 0
	for _, m := range rs[0].Members {
		if m.FallbackFrom != nil {
			fell++
			noWindowOf(t, "members[].fallback_from.error", m.FallbackFrom.Error, v)
		}
	}
	if fell != 1 {
		t.Fatalf("members %+v", rs[0].Members)
	}
	raw, err = json.Marshal(rs[0])
	if err != nil {
		t.Fatal(err)
	}
	noWindowOf(t, "the panel gate.run record", string(raw), v)
}
