package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

type key struct{ pkg, test string }

type Skip struct {
	Package, Test, Output string
}

type Allowlist map[key]string

func ParseAllowlist(r io.Reader) (Allowlist, error) {
	allow := Allowlist{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			return nil, fmt.Errorf("allowlist line %d: want `<package> <TestName[/subtest]> <reason>`, got %q (a reason is required)", n, line)
		}
		k := key{f[0], f[1]}
		if _, dup := allow[k]; dup {
			return nil, fmt.Errorf("allowlist line %d: duplicate entry for %s %s", n, k.pkg, k.test)
		}
		allow[k] = strings.Join(f[2:], " ")
	}
	return allow, sc.Err()
}

func loadAllowlist(path string) (Allowlist, error) {
	if path == "" {
		return Allowlist{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	allow, err := ParseAllowlist(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return allow, nil
}

func Scan(r io.Reader, w io.Writer, allow Allowlist, watch *Watch) ([]Skip, error) {
	var (
		bad  []Skip
		seen = map[key]bool{}
		buf  = map[key][]string{}
		rd   = bufio.NewReader(r)
	)
	for {
		line, err := rd.ReadString('\n')
		if line != "" {
			var ev event
			if json.Unmarshal([]byte(line), &ev) != nil || ev.Action == "" {
				io.WriteString(w, line)
			} else {
				io.WriteString(w, ev.Output)
				watch.observe(ev, time.Now())
				if ev.Test != "" {
					k := key{ev.Package, ev.Test}
					switch ev.Action {
					case "output":
						buf[k] = append(buf[k], ev.Output)
					case "skip":
						if _, ok := allow[k]; !ok && !seen[k] {
							seen[k] = true
							bad = append(bad, Skip{ev.Package, ev.Test, skipOutput(buf[k])})
						}
						delete(buf, k)
					case "pass", "fail":
						delete(buf, k)
					}
				}
			}
		}
		if err == io.EOF {
			return bad, nil
		}
		if err != nil {
			return bad, err
		}
	}
}

type Stalled struct {
	Package, Test string
	Running       time.Duration
}

type Watch struct {
	stall time.Duration

	mu      sync.Mutex
	live    map[key]time.Time
	stalled []Stalled
}

func newWatch(stall time.Duration) *Watch {
	return &Watch{stall: stall, live: map[key]time.Time{}}
}

func (w *Watch) observe(ev event, now time.Time) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if ev.Test == "" {
		switch ev.Action {
		case "pass", "fail", "skip":
			for k := range w.live {
				if k.pkg == ev.Package {
					delete(w.live, k)
				}
			}
		}
		return
	}
	switch ev.Action {
	case "run", "cont":
		w.live[key{ev.Package, ev.Test}] = now
	case "pause", "pass", "fail", "skip":
		delete(w.live, key{ev.Package, ev.Test})
	}
	for i, c := range ev.Test {
		if c != '/' {
			continue
		}
		parent := key{ev.Package, ev.Test[:i]}
		if _, running := w.live[parent]; running {
			w.live[parent] = now
		}
	}
}

func (w *Watch) expired(now time.Time) []Stalled {
	w.mu.Lock()
	defer w.mu.Unlock()
	var hung []Stalled
	for k, since := range w.live {
		if d := now.Sub(since); d > w.stall {
			hung = append(hung, Stalled{k.pkg, k.test, d})
		}
	}
	sort.Slice(hung, func(i, j int) bool {
		if hung[i].Package != hung[j].Package {
			return hung[i].Package < hung[j].Package
		}
		return hung[i].Test < hung[j].Test
	})
	return hung
}

func (w *Watch) patrol(stop <-chan struct{}, kill func()) {
	period := min(max(w.stall/4, 10*time.Millisecond), time.Second)
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-tick.C:
			if hung := w.expired(now); len(hung) > 0 {
				w.mu.Lock()
				w.stalled = hung
				w.mu.Unlock()
				kill()
				return
			}
		}
	}
}

func (w *Watch) hung() []Stalled {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stalled
}

func reportStalled(w io.Writer, hung []Stalled, stall time.Duration) {
	fmt.Fprintf(w, "\nskipcheck: %d test(s) stalled, running more than %s with no result:\n", len(hung), stall)
	for _, h := range hung {
		fmt.Fprintf(w, "  %s %s: running %s\n", h.Package, h.Test, h.Running.Round(time.Millisecond))
	}
	fmt.Fprintln(w, "skipcheck: killed the test process group; a hung test fails the run by name.")
}

func skipOutput(lines []string) string {
	var out []string
	for _, l := range strings.Split(strings.Join(lines, ""), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "=== ") {
			out = append(out, l)
		}
	}
	return strings.Join(out, " | ")
}

func report(w io.Writer, bad []Skip) {
	sort.Slice(bad, func(i, j int) bool {
		if bad[i].Package != bad[j].Package {
			return bad[i].Package < bad[j].Package
		}
		return bad[i].Test < bad[j].Test
	})
	fmt.Fprintf(w, "\nskipcheck: %d skip(s) not on the allowlist:\n", len(bad))
	for _, s := range bad {
		fmt.Fprintf(w, "  %s %s: %s\n", s.Package, s.Test, s.Output)
	}
	fmt.Fprintln(w, "skipcheck: fix the skip, or add `<package> <test> <reason>` to the allowlist after review.")
}

type treeState struct {
	head  string
	files map[string]string
}

func snapshotTree(dir string) (treeState, error) {
	git := func(args ...string) ([]byte, error) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return out, nil
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return treeState{}, err
	}
	status, err := git("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return treeState{}, err
	}
	st := treeState{head: strings.TrimSpace(string(head)), files: map[string]string{}}
	entries := strings.Split(string(status), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		code, path := e[:2], e[3:]
		if strings.ContainsAny(code, "RC") {
			i++
		}
		st.files[path] = code + " " + fingerprint(filepath.Join(dir, path))
	}
	return st, nil
}

func fingerprint(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "deleted"
	}
	var body []byte
	if info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		body = []byte(target)
	} else if body, err = os.ReadFile(path); err != nil {
		return "unreadable"
	}
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func diffTree(before, after treeState) []string {
	var out []string
	if before.head != after.head {
		out = append(out, fmt.Sprintf("moved HEAD %s -> %s", before.head, after.head))
	}
	untracked := func(state string) bool { return strings.HasPrefix(state, "?? ") }
	seen := map[string]bool{}
	for p, b := range before.files {
		seen[p] = true
		switch a, ok := after.files[p]; {
		case !ok && untracked(b):
			out = append(out, "vanished "+p)
		case !ok, a != b:
			out = append(out, "changed "+p)
		}
	}
	for p, a := range after.files {
		if seen[p] {
			continue
		}
		if untracked(a) {
			out = append(out, "appeared "+p)
		} else {
			out = append(out, "changed "+p)
		}
	}
	sort.Strings(out)
	return out
}

func reportTree(w io.Writer, changes []string) {
	fmt.Fprintln(w, "\nskipcheck: the test run changed the source tree:")
	for _, c := range changes {
		fmt.Fprintf(w, "  %s\n", c)
	}
	fmt.Fprintln(w, "skipcheck: a test must write only to a temp directory; a source-walking test in another package races a file planted in the tree.")
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("skipcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	allowPath := fs.String("allow", "", "allowed-skip list (`<package> <test> <reason>` per line)")
	stall := fs.Duration("stall", 0, "fail the run, and kill the child's process group, when a test runs this long with no result (0 = off)")
	tree := fs.String("tree", "", "git working tree `DIR` the run must leave as it found it: fail when the run adds, changes or removes a file there (empty = off)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cmdline := fs.Args()
	if len(cmdline) == 0 {
		fmt.Fprintln(stderr, "usage: skipcheck [-allow FILE] [-stall DURATION] [-tree DIR] -- go test -json [flags] [packages]")
		return 2
	}
	if *stall < 0 {
		fmt.Fprintln(stderr, "skipcheck: -stall must not be negative")
		return 2
	}
	allow, err := loadAllowlist(*allowPath)
	if err != nil {
		fmt.Fprintf(stderr, "skipcheck: %v\n", err)
		return 2
	}

	var before treeState
	if *tree != "" {
		if before, err = snapshotTree(*tree); err != nil {
			fmt.Fprintf(stderr, "skipcheck: -tree %s: %v\n", *tree, err)
			return 2
		}
	}

	child := exec.Command(cmdline[0], cmdline[1:]...)
	ownGroup(child)
	child.Stdin = os.Stdin
	child.Stderr = stderr
	out, err := child.StdoutPipe()
	if err != nil {
		fmt.Fprintf(stderr, "skipcheck: %v\n", err)
		return 2
	}
	if err := child.Start(); err != nil {
		fmt.Fprintf(stderr, "skipcheck: %v\n", err)
		return 2
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	finished := make(chan struct{})
	defer func() { signal.Stop(sigs); close(finished) }()
	go func() {
		for {
			select {
			case sig := <-sigs:
				signalGroup(child, sig)
			case <-finished:
				return
			}
		}
	}()

	var watch *Watch
	stopPatrol, patrolled := make(chan struct{}), make(chan struct{})
	if *stall > 0 {
		watch = newWatch(*stall)
		go func() {
			defer close(patrolled)
			watch.patrol(stopPatrol, func() { signalGroup(child, os.Kill) })
		}()
	} else {
		close(patrolled)
	}
	bad, scanErr := Scan(out, stdout, allow, watch)
	if scanErr != nil {
		fmt.Fprintf(stderr, "skipcheck: reading child output: %v\n", scanErr)
	}
	close(stopPatrol)
	<-patrolled
	waitErr := child.Wait()

	if len(bad) > 0 {
		report(stderr, bad)
	}
	var hung []Stalled
	if watch != nil {
		if hung = watch.hung(); len(hung) > 0 {
			reportStalled(stderr, hung, *stall)
		}
	}
	var changed []string
	if *tree != "" {
		after, err := snapshotTree(*tree)
		if err != nil {
			fmt.Fprintf(stderr, "skipcheck: -tree %s: %v\n", *tree, err)
			return 2
		}
		if changed = diffTree(before, after); len(changed) > 0 {
			reportTree(stderr, changed)
		}
	}
	var ee *exec.ExitError
	switch {
	case len(hung) > 0:
		return 1
	case errors.As(waitErr, &ee):
		if code := ee.ExitCode(); code > 0 {
			return code
		}
		return 1
	case waitErr != nil, scanErr != nil:
		return 2
	case len(bad) > 0, len(changed) > 0:
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
