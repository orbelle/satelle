//go:build integration

package tests

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// The steady budget satelled must fit with 26 registered repos, 6 of them
// taking a CLI change every few seconds (sty_a003a459). Children it spawns
// (reseed, substrate, follow) are separate processes: reported, not budgeted.
const (
	cpuBudget = 0.10      // of one core
	rssBudget = 300 << 20 // bytes
)

// Load shape.
const (
	loadRepos      = 26
	loadActive     = 6
	loadStories    = 500
	loadEditEvery  = 3 * time.Second
	loadWarmup     = 30 * time.Second
	loadRSSSample  = 5 * time.Second
	loadProfileSec = 60
)

// loadWindow is the steady window: longer than one default reconcile interval,
// so it holds at least one full pass over every target. SATELLE_LOAD_WINDOW
// shortens it for a local try; such a run is logged as not evidence.
func loadWindow(t *testing.T) time.Duration {
	w := mirror.DefaultReconcileInterval + time.Minute
	if v := os.Getenv("SATELLE_LOAD_WINDOW"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("SATELLE_LOAD_WINDOW: %v", err)
		}
		w = d
	}
	if w <= mirror.DefaultReconcileInterval {
		t.Logf("window %s is not longer than the reconcile interval %s: this run is not evidence", w, mirror.DefaultReconcileInterval)
	}
	return w
}

// TestSatelledSteadyLoadFitsBudget runs a real satelled against 26 real
// initialised repos registered through `satelle workspace add`; six take a real
// `satelle story set` every 3 s. satelled's CPU comes from /proc/<pid>/stat,
// its RSS from /proc/<pid>/status and a profile from its loopback pprof. The
// numbers and a `go tool pprof -top` summary go to load-budget.md under
// SATELLE_LOAD_OUT (else a temp dir). Gated by SATELLE_LOAD=1.
func TestSatelledSteadyLoadFitsBudget(t *testing.T) {
	if os.Getenv("SATELLE_LOAD") != "1" {
		t.Skip("load benchmark: set SATELLE_LOAD=1")
	}
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	window := loadWindow(t)
	out := os.Getenv("SATELLE_LOAD_OUT")
	if out == "" {
		out = t.TempDir()
	}

	// satelled resolves its CLI as a sibling of its own executable.
	binDir := t.TempDir()
	daemon := filepath.Join(binDir, "satelled")
	build := exec.Command("go", "build", "-o", daemon, "./cmd/satelled")
	build.Dir = moduleRoot()
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build satelled: %v\n%s", err, b)
	}
	if err := os.Symlink(testBin, filepath.Join(binDir, "satelle")); err != nil {
		t.Fatal(err)
	}

	home := isolatedHome(t)
	port := freeListenPort(t)
	host := "http://127.0.0.1:" + port
	writeFile(t, filepath.Join(home, "config.toml"), fmt.Sprintf("[service]\nendpoint = %q\nport = %s\n", host, port))

	var daemonLog lockedBuffer
	d := startSatelled(t, daemon, home, port, &daemonLog)
	if !waitHealthy(t, host+"/healthz", 10*time.Second) {
		t.Fatalf("satelled never became healthy:\n%s", daemonLog.String())
	}
	pid := d.Process.Pid

	// 26 real repos; the first six carry the card's 500 stories.
	repos := make([]string, loadRepos)
	for i := range repos {
		repo := t.TempDir()
		if r, err := filepath.EvalSymlinks(repo); err == nil {
			repo = r
		}
		if b, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, b)
		}
		mustRun(t, testBin, repo, "init")
		repos[i] = repo
	}
	ids := make([][]string, loadActive)
	parallel(t, loadActive, func(i int) error {
		for n := 0; n < loadStories; n++ {
			o, err := runEnv(t, testBin, repos[i], nil, "story", "create",
				"--title", fmt.Sprintf("load %d", n), "--body", "load", "--acceptance", "1. x", "--category", "chore")
			if err != nil {
				return fmt.Errorf("repo %d story %d: %v\n%s", i, n, err, o)
			}
			ids[i] = append(ids[i], extractID(o, "sty_"))
		}
		return nil
	})
	for _, repo := range repos {
		seedWorkspaceAdd(t, testBin, repo, host)
	}

	// Six editors, each a real CLI verb every loadEditEvery, until the window ends.
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var edits, editErrs sync.Map
	var wg sync.WaitGroup
	priorities := []string{"low", "medium", "high"}
	for i := 0; i < loadActive; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tick := time.NewTicker(loadEditEvery)
			defer tick.Stop()
			for n := 0; ; n++ {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
				id := ids[i][n%len(ids[i])]
				if o, err := runEnv(t, testBin, repos[i], []string{"SATELLE_SERVER_ENDPOINT=" + host},
					"story", "set", id, "--priority", priorities[n%len(priorities)]); err != nil {
					editErrs.Store(fmt.Sprintf("%d/%d", i, n), fmt.Sprintf("%v: %s", err, o))
					continue
				}
				v, _ := edits.LoadOrStore(i, new(int))
				*v.(*int)++
			}
		}(i)
	}
	defer wg.Wait()

	time.Sleep(loadWarmup) // time-subject: the benchmark's warm-up before the steady window

	start := readProcStat(t, pid)
	t0 := time.Now()
	logStart := daemonLog.Len()
	profile := make(chan error, 1)
	cpuProfile := filepath.Join(out, "satelled-cpu.pprof")
	go func() {
		profile <- fetchTo(host+fmt.Sprintf("/debug/pprof/profile?seconds=%d", loadProfileSec), cpuProfile, loadProfileSec+30)
	}()
	var maxRSS int64
	for deadline := t0.Add(window); time.Now().Before(deadline); {
		if rss := readRSS(t, pid); rss > maxRSS {
			maxRSS = rss
		}
		time.Sleep(loadRSSSample) // time-subject: the RSS sample interval inside the steady window
	}
	end := readProcStat(t, pid)
	elapsed := time.Since(t0)
	stop()
	wg.Wait()

	heapProfile := filepath.Join(out, "satelled-heap.pprof")
	if err := fetchTo(host+"/debug/pprof/heap", heapProfile, 30); err != nil {
		t.Errorf("heap profile: %v", err)
	}
	if err := <-profile; err != nil {
		t.Errorf("cpu profile: %v", err)
	}

	tck := clockTicks()
	cpu := float64(end.self-start.self) / tck / elapsed.Seconds()
	childCPU := float64(end.children-start.children) / tck / elapsed.Seconds()
	windowLog := daemonLog.String()[logStart:]
	totalEdits := 0
	edits.Range(func(_, v any) bool { totalEdits += *v.(*int); return true })
	var failed []string
	editErrs.Range(func(k, v any) bool { failed = append(failed, k.(string)+": "+v.(string)); return true })

	var rep strings.Builder
	fmt.Fprintf(&rep, "# satelled steady-load budget (sty_a003a459)\n\n")
	fmt.Fprintf(&rep, "Shape: %d repos registered through `satelle workspace add`, %d with %d stories each taking a `satelle story set --priority` every %s; warm-up %s, steady window %s (reconcile interval %s).\n\n",
		loadRepos, loadActive, loadStories, loadEditEvery, loadWarmup, elapsed.Round(time.Second), mirror.DefaultReconcileInterval)
	fmt.Fprintf(&rep, "| measure | budget | measured |\n|---|---|---|\n")
	fmt.Fprintf(&rep, "| satelled CPU (utime+stime, /proc/<pid>/stat) | %.0f%% of one core | %.2f%% |\n", cpuBudget*100, cpu*100)
	fmt.Fprintf(&rep, "| satelled max RSS (VmRSS, /proc/<pid>/status, every %s) | %d MB | %.1f MB |\n", loadRSSSample, rssBudget>>20, float64(maxRSS)/(1<<20))
	fmt.Fprintf(&rep, "| reaped children CPU (cutime+cstime), not budgeted | — | %.2f%% |\n\n", childCPU*100)
	fmt.Fprintf(&rep, "Edits landed in the run: %d (failed %d). Children spawned in the window, from satelled's log: reseed passes %d, dirty/failed retries %d, substrate legs %d.\n\n",
		totalEdits, len(failed), strings.Count(windowLog, "sync reseed "), strings.Count(windowLog, "sync retry "),
		strings.Count(windowLog, "sync substrate "))
	fmt.Fprintf(&rep, "## CPU profile (%ds, `go tool pprof -top`)\n\n```\n%s```\n\n", loadProfileSec, pprofTop(daemon, cpuProfile))
	fmt.Fprintf(&rep, "## Heap profile (`go tool pprof -top -sample_index=inuse_space`)\n\n```\n%s```\n", pprofTop(daemon, heapProfile, "-sample_index=inuse_space"))
	if err := os.WriteFile(filepath.Join(out, "load-budget.md"), []byte(rep.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("report: %s\n%s", filepath.Join(out, "load-budget.md"), rep.String())

	if len(failed) > 0 {
		t.Errorf("%d CLI edit(s) failed, first: %s", len(failed), failed[0])
	}
	if cpu >= cpuBudget {
		t.Errorf("satelled steady CPU %.2f%% of one core, budget %.0f%%", cpu*100, cpuBudget*100)
	}
	if maxRSS >= rssBudget {
		t.Errorf("satelled max RSS %.1f MB, budget %d MB", float64(maxRSS)/(1<<20), rssBudget>>20)
	}
}

// startSatelled runs the daemon binary itself (not the `satelle serve` alias)
// under home with the endpoint off-switch dropped, so its reconcile runs.
func startSatelled(t *testing.T, bin, home, port string, out io.Writer) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin, "--addr", "127.0.0.1", "--port", port)
	cmd.Env = []string{"SATELLE_HOME=" + home}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SATELLE_SERVER_ENDPOINT=") && !strings.HasPrefix(kv, "SATELLE_HOME=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Stdout, cmd.Stderr = out, out
	setServeProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start satelled: %v", err)
	}
	h := &ServeHandle{Cmd: cmd}
	registerServe(cmd.Process.Pid, bin)
	t.Cleanup(h.Stop)
	return cmd
}

// parallel runs fn for 0..n-1 concurrently and fails on the first error.
func parallel(t *testing.T, n int, fn func(i int) error) {
	t.Helper()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = fn(i) }(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// procTicks is a process's own and reaped children's CPU in clock ticks.
type procTicks struct{ self, children int64 }

// readProcStat reads utime+stime and cutime+cstime (fields 14-17).
func readProcStat(t *testing.T, pid int) procTicks {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	// Fields after the parenthesised comm start at field 3 (state).
	rest := string(b[bytes.LastIndexByte(b, ')')+2:])
	f := strings.Fields(rest)
	n := func(field int) int64 {
		v, err := strconv.ParseInt(f[field-3], 10, 64)
		if err != nil {
			t.Fatalf("/proc/%d/stat field %d: %v", pid, field, err)
		}
		return v
	}
	return procTicks{self: n(14) + n(15), children: n(16) + n(17)}
}

// readRSS reads VmRSS in bytes.
func readRSS(t *testing.T, pid int) int64 {
	t.Helper()
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return kb << 10
		}
	}
	t.Fatalf("/proc/%d/status has no VmRSS", pid)
	return 0
}

// clockTicks is CLK_TCK; 100 when getconf is unavailable.
func clockTicks() float64 {
	if b, err := exec.Command("getconf", "CLK_TCK").Output(); err == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil && v > 0 {
			return v
		}
	}
	return 100
}

// fetchTo saves url's body to path within timeoutSec.
func fetchTo(url, path string, timeoutSec int) error {
	c := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// pprofTop is the first lines of `go tool pprof -top` for a profile.
func pprofTop(bin, profile string, extra ...string) string {
	args := append([]string{"tool", "pprof", "-top", "-nodecount=20"}, extra...)
	b, err := exec.Command("go", append(args, bin, profile)...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("pprof failed: %v\n%s", err, b)
	}
	return string(b)
}

// lockedBuffer is a bytes.Buffer safe for the daemon's writes and the test's reads.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuffer) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Len()
}
