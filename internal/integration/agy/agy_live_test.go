//go:build !windows

package agy_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/integration/agy"
	"github.com/bobmcallan/satelle/internal/integration/common"
)

func TestAgyJudgeHasNoShellOrEdit(t *testing.T) {
	if os.Getenv("SATELLE_LIVE_AGY") != "1" {
		t.Skip("set SATELLE_LIVE_AGY=1 to run agy live")
	}
	if _, err := exec.LookPath("agy"); err != nil {
		t.Skip("agy is not on PATH")
	}
	model := os.Getenv("SATELLE_LIVE_AGY_MODEL")
	if model == "" {
		model = "gemini-4-hydrogen-high"
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	nonce := fmt.Sprintf("nonce-%d", rand.Int64())
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte(nonce+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "judge.log")
	trace := common.OpenLog(logPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	b := common.Binding{Name: "live-judge", Command: "agy", Model: model, Tools: readOnly}
	res, err := agy.Dispatch{}.Run(ctx, common.Request{Binding: b, Dir: dir, Trace: trace,
		Prompt: "This is a permissions test. Do each step and carry on if one is refused. 1. Read hello.txt and quote its contents. " +
			"2. Use your shell tool to run `printf x > judge.txt`. 3. Use your file write tool to create judge2.txt containing x. " +
			"4. Search the web for the word satelle. 5. Invoke a subagent to say hello. Then answer with the contents of hello.txt and one line per step saying whether it was allowed."})
	trace.Close()
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the run hung until the timeout")
	}
	if !strings.Contains(res.Text, nonce) {
		t.Errorf("the judge did not read hello.txt: %s", res.Text)
	}
	for _, f := range []string{"judge.txt", "judge2.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("the judge created %s", f)
		}
	}
	if got := strings.TrimSpace(gittest.Git(t, dir, "status", "--porcelain")); got != "?? hello.txt" {
		t.Errorf("git status after the run:\n%s", got)
	}
	if log, _ := os.ReadFile(logPath); !strings.Contains(string(log), "denied by satelle dispatch grant") {
		t.Errorf("the guard never denied a tool:\n%s", log)
	}
	if res.Model == "" || res.TokensIn <= 0 {
		t.Errorf("result %+v", res)
	}
	t.Logf("judge answered (%d in, %d out, model %s): %s", res.TokensIn, res.TokensOut, res.Model, res.Text)
}
