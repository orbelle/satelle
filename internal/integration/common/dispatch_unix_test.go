//go:build !windows

package common

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
)

func TestRunOnceErrorTailIsRedacted(t *testing.T) {
	b := make([]byte, 100)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	v := hex.EncodeToString(b)
	t.Setenv("SATELLE_TEST_SECRET", v)
	t.Setenv("PRE", strings.Repeat("a", 900))
	t.Setenv("POST", strings.Repeat("b", 1900))
	argv := []string{"sh", "-c", `printf '%s' "$PRE$SATELLE_TEST_SECRET$POST" >&2; exit 1`}
	req := Request{Dir: t.TempDir(), Redact: func(s string) string { return strings.ReplaceAll(s, v, "[secret]") }}
	_, err := RunOnce(context.Background(), argv, "", req)
	if err == nil {
		t.Fatal("a failing command returned no error")
	}
	if !strings.Contains(err.Error(), "[secret]") {
		t.Fatalf("the error holds no [secret]: %d bytes", len(err.Error()))
	}
	for i := 0; i+16 <= len(v); i++ {
		if strings.Contains(err.Error(), v[i:i+16]) {
			t.Fatalf("the error holds a 16-byte window of the value at %d", i)
		}
	}
	if got := (Request{}).Clean("x" + v); got != "x"+v {
		t.Fatal("Clean with no redactor changed the text")
	}
}

func TestEnvironTarget(t *testing.T) {
	t.Setenv("SATELLE_TEST_CALLER", "caller")
	self := Environ(Request{Env: []string{"A=1"}})
	if self[len(self)-1] != "A=1" || !slices.Contains(self, "SATELLE_TEST_CALLER=caller") {
		t.Fatalf("self environ lacks the caller or req.Env")
	}
	tgt := Environ(Request{Env: []string{"A=1"}, Target: &Target{Environ: []string{"PATH=/bin"}}})
	if strings.Join(tgt, " ") != "PATH=/bin A=1" {
		t.Fatalf("target environ %q", tgt)
	}
	if e := Environ(Request{Target: &Target{Environ: []string{}}}); e == nil || len(e) != 0 {
		t.Fatalf("empty target environ %#v", e)
	}
	_, err := RunOnce(context.Background(), []string{"true"}, "", Request{Target: &Target{}})
	if err != ErrUnconfined {
		t.Fatalf("unconfined target ran: %v", err)
	}
	clean := Chain(func(s string) string { return strings.ReplaceAll(s, "kernel", "[k]") }, ValueRedactor([]string{"abc", "abcdef", ""}))
	if got := clean("kernel abcdef abc"); got != "[k] [secret] [secret]" {
		t.Fatalf("redacted %q", got)
	}
	if ValueRedactor(nil) != nil || Chain(nil, nil) != nil {
		t.Fatal("empty redactor is not nil")
	}
}
