package tests

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startSatelled runs the satelled binary on a free loopback port in the
// sandbox's home, points the sandbox's CLI at it and returns its base URL.
func startSatelled(t *testing.T, s *sandbox) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, satelledBin, "--addr", "127.0.0.1", "--port", fmt.Sprint(port))
	cmd.Dir = s.dir
	cmd.Env = s.environ()
	var log strings.Builder
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(url + "/")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("satelled did not answer at %s: %v\n%s", url, err, log.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.env["SATELLE_SERVER_ENDPOINT"] = url
	return url
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s\n%s", url, resp.Status, b)
	}
	return string(b)
}

// satelled shows what the CLI pushes: the story list, and on the detail page
// the route, the verdicts and the attachments.
func TestSatelledShowsTheStory(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	url := startSatelled(t, s)
	s.must("init")
	s.write(".satelle/skills/has-ok.md", "---\ntype: check\n---\n```sh\ntest -f ok.txt || { echo ok.txt is missing; exit 1; }\n```\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.backlog]
start = true
[states.done]
terminal = true
[gates.has-ok]
skill = "has-ok"
[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["has-ok"] }]
`)
	if out := s.must("workspace", "add"); !strings.Contains(out, "seeded satelled at "+url) {
		t.Fatalf("workspace add: %s", out)
	}
	out := s.must("workspace", "partitions")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("partitions:\n%s", out)
	}
	key := strings.Fields(lines[1])[0]

	id := s.create("story", "--title", "seen in satelled", "--criteria", "shows up").ID
	if r := s.run("story", "set", id, "--status", "done"); r.code != 2 {
		t.Fatalf("the first move: exit %d", r.code)
	}
	s.write("ok.txt", "")
	s.write("evidence.md", "# evidence\n")
	s.must("story", "attach", id, "--file", "evidence.md")
	s.must("story", "set", id, "--status", "done")

	list := httpGet(t, url+"/r/"+key+"/fragment/stories")
	if !strings.Contains(list, "seen in satelled") || !strings.Contains(list, id) {
		t.Errorf("story list lacks the story:\n%s", list)
	}
	page := httpGet(t, url+"/r/"+key+"/story/"+id)
	for _, want := range []string{"seen in satelled", "evidence.md", "has-ok", "ok.txt is missing", "outcome-refused", "outcome-moved", "st-terminal"} {
		if !strings.Contains(page, want) {
			t.Errorf("story page lacks %q", want)
		}
	}
}
