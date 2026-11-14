//go:build !windows

package ledgerstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// stub writes a sh store command that records its argv (one per line), its
// stdin and each invocation beside it, then runs script. It returns a Command
// running it through sh with two configured args.
func stub(t *testing.T, script string) (Command, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "store")
	body := `d=$(dirname "$0")
echo run >> "$d/calls"
printf '%s\n' "$@" > "$d/argv"
cat > "$d/stdin"
shift 2
` + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Command{Path: "sh", Args: []string{path, "--root", "r"}, Dir: dir}, dir
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func calls(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "calls"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "run\n")
}

var ctx = context.Background()

func TestPutIfAbsentCreatedRunsOnceWithArgsOpAndBody(t *testing.T) {
	c, dir := stub(t, "exit 0")
	existing, created, err := c.PutIfAbsent(ctx, "chains/x/000000000001.json", []byte("body\n"))
	if err != nil || !created || existing != nil {
		t.Fatalf("got %q %v %v, want created", existing, created, err)
	}
	if got, want := readLines(t, filepath.Join(dir, "argv")), []string{"--root", "r", "put-if-absent", "chains/x/000000000001.json"}; !slices.Equal(got, want) {
		t.Errorf("argv %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "stdin")); string(b) != "body\n" {
		t.Errorf("stdin %q", b)
	}
	if n := calls(t, dir); n != 1 {
		t.Errorf("ran %d times, want once", n)
	}
}

func TestPutIfAbsentExistsReturnsTheExistingBody(t *testing.T) {
	c, _ := stub(t, `printf 'old body'; exit 3`)
	existing, created, err := c.PutIfAbsent(ctx, "k", []byte("new"))
	if err != nil || created || string(existing) != "old body" {
		t.Fatalf("got %q %v %v, want the existing body", existing, created, err)
	}
}

func TestGetFoundAndNotFound(t *testing.T) {
	c, dir := stub(t, `printf 'the body'; exit 0`)
	body, found, err := c.Get(ctx, "k")
	if err != nil || !found || string(body) != "the body" {
		t.Fatalf("got %q %v %v", body, found, err)
	}
	if got := readLines(t, filepath.Join(dir, "argv")); !slices.Equal(got, []string{"--root", "r", "get", "k"}) {
		t.Errorf("argv %q", got)
	}
	c, _ = stub(t, "exit 4")
	body, found, err = c.Get(ctx, "k")
	if err != nil || found || body != nil {
		t.Fatalf("got %q %v %v, want not found with a nil error", body, found, err)
	}
}

func TestListReadsSortedKeys(t *testing.T) {
	c, dir := stub(t, `printf 'p/a\np/b\np/c\n'`)
	keys, err := c.List(ctx, "p/", "")
	if err != nil || !slices.Equal(keys, []string{"p/a", "p/b", "p/c"}) {
		t.Fatalf("got %q %v", keys, err)
	}
	if got := readLines(t, filepath.Join(dir, "argv")); !slices.Equal(got, []string{"--root", "r", "list", "p/"}) {
		t.Errorf("argv %q", got)
	}
	c, dir = stub(t, `printf 'p/b\np/c\n'`)
	if keys, err := c.List(ctx, "p/", "p/a"); err != nil || len(keys) != 2 {
		t.Fatalf("got %q %v", keys, err)
	}
	if got := readLines(t, filepath.Join(dir, "argv")); !slices.Equal(got, []string{"--root", "r", "list", "p/", "p/a"}) {
		t.Errorf("argv with after-key %q", got)
	}
	c, _ = stub(t, "exit 0")
	keys, err = c.List(ctx, "p/", "")
	if err != nil || keys == nil || len(keys) != 0 {
		t.Fatalf("an empty list: got %#v %v, want an empty slice", keys, err)
	}
}

func TestListOutputThatBreaksTheContractIsAProtocolError(t *testing.T) {
	for _, tc := range []struct{ name, out, after, want string }{
		{"unsorted", `p/b\np/a\n`, "", "does not sort after"},
		{"duplicate", `p/a\np/a\n`, "", "does not sort after"},
		{"outside the prefix", `p/a\nq/b\n`, "", "is not under the prefix"},
		{"not after the after-key", `p/a\np/b\n`, "p/a", "is not after the after-key"},
		{"not a key", `p/a b\n`, "", "is not a key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := stub(t, `printf '`+tc.out+`'`)
			_, err := c.List(ctx, "p/", tc.after)
			var se *Error
			if !errors.As(err, &se) || se.Retryable || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "broke the contract") {
				t.Fatalf("got %v, want a permanent protocol error naming %q", err, tc.want)
			}
		})
	}
}

func TestExitOneIsTransientAndExitTwoPermanentWithTheirLine(t *testing.T) {
	c, _ := stub(t, `echo noise >&2; echo 'error: unavailable: throttled' >&2; exit 1`)
	_, _, err := c.Get(ctx, "k")
	var se *Error
	if !errors.As(err, &se) || !se.Retryable || se.Exit != 1 || se.Code != "unavailable" || se.Line != "error: unavailable: throttled" {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(err.Error(), "error: unavailable: throttled") || !strings.Contains(err.Error(), "exit 1 (transient)") || strings.Contains(err.Error(), "noise") {
		t.Errorf("Error() = %q", err)
	}
	c, _ = stub(t, `echo 'error: auth: access denied' >&2; exit 2`)
	_, _, err = c.PutIfAbsent(ctx, "k", nil)
	if !errors.As(err, &se) || se.Retryable || se.Exit != 2 || se.Code != "auth" {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(err.Error(), "error: auth: access denied") || !strings.Contains(err.Error(), "exit 2 (permanent)") {
		t.Errorf("Error() = %q", err)
	}
	c, _ = stub(t, `echo 'error: invalid: bad' >&2; exit 2`)
	if _, err := c.List(ctx, "p/", ""); !errors.As(err, &se) || se.Code != "invalid" {
		t.Errorf("list exit 2: %v", err)
	}
}

func TestEveryContractCodeIsACauseAndNoOtherIs(t *testing.T) {
	for _, code := range []string{"auth", "not-found", "conflict", "unavailable", "invalid"} {
		c, _ := stub(t, `echo 'error: `+code+`: why' >&2; exit 2`)
		_, _, err := c.Get(ctx, "k")
		var se *Error
		if !errors.As(err, &se) || se.Code != code || se.Line != "error: "+code+": why" {
			t.Errorf("code %s: got %v", code, err)
		}
	}
	for _, line := range []string{"error: bogus: x", "error: auth:", "Error: auth: x", " error: auth: x"} {
		c, _ := stub(t, `echo '`+line+`' >&2; exit 2`)
		_, _, err := c.Get(ctx, "k")
		var se *Error
		if !errors.As(err, &se) || se.Line != "" || !strings.Contains(err.Error(), "printed no error: <code>: <message> line") {
			t.Errorf("line %q: got %v, want no cause line", line, err)
		}
	}
}

func TestEnvironmentIsTheParentsMinusWithheldNothingAdded(t *testing.T) {
	t.Setenv("SATELLE_LEDGERSTORE_SENTINEL", "s3cr3t")
	t.Setenv("SATELLE_TOKEN", "tok")
	c, dir := stub(t, `env > "$d/env"`)
	c.Withhold = []string{"SATELLE_TOKEN"}
	if _, _, err := c.Get(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	child := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if n, _, ok := strings.Cut(l, "="); ok && !strings.ContainsAny(n, " \t") && n != "" {
			child[n] = true
		}
	}
	if child["SATELLE_TOKEN"] {
		t.Error("the withheld SATELLE_TOKEN reached the command")
	}
	if !child["SATELLE_LEDGERSTORE_SENTINEL"] {
		t.Error("the parent's own variable did not reach the command")
	}
	// sh itself sets PWD, SHLVL and _; nothing else may be added.
	for n := range child {
		if _, ok := os.LookupEnv(n); !ok && n != "PWD" && n != "SHLVL" && n != "_" && n != "OLDPWD" {
			t.Errorf("the command saw %s, which the parent does not have", n)
		}
	}
}

func TestErrorNeverCarriesTheEnvironmentOrOutput(t *testing.T) {
	t.Setenv("SATELLE_LEDGERSTORE_SENTINEL", "s3cr3t")
	t.Setenv("SATELLE_TOKEN", "tok")
	c, _ := stub(t, `env; env >&2; echo 'error: auth: denied' >&2; exit 2`)
	_, _, err := c.Get(ctx, "k")
	if err == nil || !strings.Contains(err.Error(), "error: auth: denied") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "tok") {
		t.Errorf("the error carries the environment: %q", err)
	}
	c, _ = stub(t, `env >&2; exit 2`)
	_, _, err = c.Get(ctx, "k")
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("an error without a cause line carries stderr: %v", err)
	}
}

func TestOtherExitCodesAreProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		call         func(Command) error
		op           string
		exit         int
	}{
		{"get exit 3", "exit 3", func(c Command) error { _, _, err := c.Get(ctx, "k"); return err }, OpGet, 3},
		{"put exit 4", "exit 4", func(c Command) error { _, _, err := c.PutIfAbsent(ctx, "k", nil); return err }, OpPutIfAbsent, 4},
		{"get exit 5", "exit 5", func(c Command) error { _, _, err := c.Get(ctx, "k"); return err }, OpGet, 5},
		{"list exit 4", "exit 4", func(c Command) error { _, err := c.List(ctx, "p/", ""); return err }, OpList, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := stub(t, tc.script)
			err := tc.call(c)
			var se *Error
			if !errors.As(err, &se) || se.Retryable || se.Exit != tc.exit || se.Op != tc.op {
				t.Fatalf("got %#v", err)
			}
			if !strings.Contains(err.Error(), "is not an outcome of "+tc.op) {
				t.Errorf("Error() = %q", err)
			}
		})
	}
}

func TestATimeoutIsTransient(t *testing.T) {
	c, _ := stub(t, "sleep 5")
	c.Timeout = 100 * time.Millisecond
	_, _, err := c.Get(ctx, "k")
	var se *Error
	if !errors.As(err, &se) || !se.Retryable || !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("got %v", err)
	}
}

func TestLimitsAreCheckedBeforeTheCommandRuns(t *testing.T) {
	c, dir := stub(t, "exit 0")
	if _, _, err := c.PutIfAbsent(ctx, "k", bytes.Repeat([]byte("x"), MaxBodyBytes+1)); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Errorf("an oversized body: %v", err)
	}
	if _, _, err := c.Get(ctx, strings.Repeat("k", MaxKeyBytes+1)); err == nil || !strings.Contains(err.Error(), "512") {
		t.Errorf("an oversized key: %v", err)
	}
	if _, err := c.List(ctx, "p q/", ""); err == nil || !strings.Contains(err.Error(), "[A-Za-z0-9._/-]") {
		t.Errorf("an invalid prefix: %v", err)
	}
	if _, err := c.List(ctx, "p/", "p/é"); err == nil || !strings.Contains(err.Error(), "[A-Za-z0-9._/-]") {
		t.Errorf("an invalid after-key: %v", err)
	}
	if _, _, err := c.PutIfAbsent(ctx, "a:b", nil); err == nil {
		t.Error("an invalid put key was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the command ran for a refused operation (%v)", err)
	}
}

func TestASignalEndNamesSignalPidAndGroup(t *testing.T) {
	for _, tc := range []struct{ sig, name string }{
		{"TERM", "SIGTERM"},
		{"KILL", "SIGKILL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := stub(t, `echo $$ > "$d/pid"; kill -`+tc.sig+` $$`)
			_, _, err := c.Get(ctx, "k")
			b, rerr := os.ReadFile(filepath.Join(dir, "pid"))
			if rerr != nil {
				t.Fatalf("the stub wrote no pid: %v (err %v)", rerr, err)
			}
			p := strings.TrimSpace(string(b))
			var se *Error
			if !errors.As(err, &se) || se.Exit != -1 || se.Retryable {
				t.Fatalf("got %#v", err)
			}
			want := "ledger store: get k (permanent): the command broke the contract: a signal ended it before it exited (signal " + tc.name + ", pid " + p + ", process group " + p + ")"
			if got := err.Error(); got != want {
				t.Errorf("Error() =\n %q\nwant\n %q", got, want)
			}
		})
	}
}

func TestANonZeroExitKeepsItsWording(t *testing.T) {
	c, _ := stub(t, "exit 3")
	_, _, err := c.Get(ctx, "k")
	if err == nil {
		t.Fatal("exit 3 from get was accepted")
	}
	want := "ledger store: get k: exit 3 (permanent): the command broke the contract: exit 3 is not an outcome of get (exit 1 is transient, 2 permanent)"
	got := err.Error()
	if got != want {
		t.Errorf("Error() =\n %q\nwant\n %q", got, want)
	}
	if strings.Contains(got, "signal") || strings.Contains(got, "process group") {
		t.Errorf("a plain exit names a signal or group: %q", got)
	}
}
