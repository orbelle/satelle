package secretref

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRef = "infisical://p/dev/ci/TOKEN"

// fakeInfisical puts a fake infisical CLI first on PATH. It records its argv,
// one argument per line, to the returned file and prints value — or, when
// fail is set, prints value on stderr and exits 1.
func fakeInfisical(t *testing.T, value string, fail bool) string {
	t.Helper()
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> " + argv + "\n"
	if fail {
		body += "printf '%s\\n' '" + value + "' >&2\nexit 1\n"
	} else {
		body += "printf '%s\\n' '" + value + "'\n"
	}
	if err := os.WriteFile(filepath.Join(dir, Binary), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argv
}

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Ref
		ok   bool
	}{
		{testRef, Ref{Project: "p", Env: "dev", Path: "/ci", Key: "TOKEN"}, true},
		{"infisical://p/dev/TOKEN", Ref{Project: "p", Env: "dev", Path: "/", Key: "TOKEN"}, true},
		{"infisical://p/prod/a/b/c/KEY", Ref{Project: "p", Env: "prod", Path: "/a/b/c", Key: "KEY"}, true},
		{"infisical://p/dev", Ref{}, false},
		{"infisical://p//ci/TOKEN", Ref{}, false},
		{"infisical://p/dev/ci/", Ref{}, false},
		{"infisical://", Ref{}, false},
		{"prefix infisical://p/dev/ci/TOKEN", Ref{}, false},
		{"infisical://p/dev/ci/TOKEN suffix", Ref{}, false},
		{"https://p/dev/ci/TOKEN", Ref{}, false},
		{"plain value", Ref{}, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
		if ok && got.String() != c.in {
			t.Errorf("Parse(%q).String() = %q", c.in, got.String())
		}
	}
}

// AC1: a reference resolves through the infisical CLI, whose argv carries only
// the reference parts.
func TestResolveThroughFakeCLI(t *testing.T) {
	const sentinel = "s3cr3t-sentinel-value-0042"
	argv := fakeInfisical(t, sentinel, false)
	ref, _ := Parse(testRef)
	got, err := Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != sentinel {
		t.Fatalf("Resolve = %q, want %q", got, sentinel)
	}
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	want := "secrets\nget\nTOKEN\n--projectId\np\n--env\ndev\n--path\n/ci\n--plain\n--silent\n"
	if string(b) != want {
		t.Errorf("argv =\n%s\nwant\n%s", b, want)
	}
	if strings.Contains(string(b), sentinel) {
		t.Error("argv carries the value")
	}
}

func TestResolveEnv(t *testing.T) {
	const sentinel = "s3cr3t-sentinel-value-0042"
	fakeInfisical(t, sentinel, false)
	in := map[string]string{"A": testRef, "B": testRef, "C": "plain"}
	out, resolved, err := ResolveEnv(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out["A"] != sentinel || out["B"] != sentinel || out["C"] != "plain" {
		t.Errorf("out = %v", out)
	}
	if len(resolved) != 1 || resolved[testRef] != sentinel {
		t.Errorf("resolved = %v, want keyed by the reference", resolved)
	}
	if in["A"] != testRef {
		t.Error("ResolveEnv mutated its input")
	}

	out, resolved, err = ResolveEnv(context.Background(), map[string]string{"C": "plain"})
	if err != nil || resolved != nil || out["C"] != "plain" {
		t.Errorf("no reference: out=%v resolved=%v err=%v", out, resolved, err)
	}
}

// AC3: an unresolvable reference fails, naming the reference and the sign-in,
// never the value.
func TestResolveFailureNamesReferenceNotValue(t *testing.T) {
	const sentinel = "s3cr3t-sentinel-value-0042"
	ref, _ := Parse(testRef)
	check := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("want an error")
		}
		var se *Error
		if !errors.As(err, &se) {
			t.Fatalf("err %T is not *Error", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, testRef) || !strings.Contains(msg, "infisical login") || !strings.Contains(msg, "INFISICAL_TOKEN") {
			t.Errorf("message lacks the reference or sign-in: %s", msg)
		}
		if strings.Contains(msg, sentinel) {
			t.Errorf("message carries the value: %s", msg)
		}
	}
	t.Run("no CLI", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := Resolve(context.Background(), ref)
		check(t, err)
	})
	t.Run("CLI fails", func(t *testing.T) {
		fakeInfisical(t, sentinel, true)
		_, err := Resolve(context.Background(), ref)
		check(t, err)
		_, _, err = ResolveEnv(context.Background(), map[string]string{"X": testRef})
		check(t, err)
	})
	t.Run("empty value", func(t *testing.T) {
		fakeInfisical(t, "", false)
		_, err := Resolve(context.Background(), ref)
		check(t, err)
	})
}
