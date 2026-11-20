//go:build integration

package tests

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestSatelledShowsTheStory(t *testing.T) {
	t.Parallel()
	s := newSandboxOutsideTemp(t)
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
	if out := s.must("project", "add"); !strings.Contains(out, "seeded satelled at "+url) {
		t.Fatalf("project add: %s", out)
	}
	out := s.must("project", "partitions")
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

func TestSatelledProjectsPageListsAddedProjects(t *testing.T) {
	t.Parallel()
	s := newSandboxOutsideTemp(t)
	url := startSatelled(t, s)
	s.must("init")
	if out := s.must("project", "add"); !strings.Contains(out, "seeded satelled at "+url) {
		t.Fatalf("project add: %s", out)
	}
	stray := `{"repo_key":"stray-0000beef","slug":"stray","identity":{"project_name":"stray","repo_root":"/srv/stray"}}`
	resp, err := http.Post(url+"/ingest/snapshot", "application/json", strings.NewReader(stray))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest the stray partition: %s %s", resp.Status, b)
	}
	landing := httpGet(t, url+"/")
	if !strings.Contains(landing, ">"+s.dir+"<") {
		t.Errorf("the Projects page lacks the added project %s:\n%s", s.dir, landing)
	}
	if strings.Contains(landing, "stray") {
		t.Errorf("the Projects page lists the stray partition of no added project:\n%s", landing)
	}
	if out := s.must("project", "partitions"); !strings.Contains(out, "not-added") || !strings.Contains(out, "stray-0000beef") {
		t.Errorf("project partitions does not mark the stray partition:\n%s", out)
	}
}

func TestSatelledIgnoresATempRepo(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.env["TMPDIR"] = filepath.Dir(s.dir)
	url := startSatelled(t, s)
	s.must("init")
	s.create("story", "--title", "never sent", "--criteria", "stays local")
	out := s.must("project", "add")
	if !strings.Contains(out, "added project") || !strings.Contains(out, "is under the temp dir") || strings.Contains(out, "seeded satelled") {
		t.Fatalf("project add in a temp-dir repo: %s", out)
	}
	parts := strings.Split(strings.TrimSpace(s.must("project", "partitions")), "\n")
	if len(parts) != 1 {
		t.Errorf("a temp-dir repo left partitions in satelled:\n%s", strings.Join(parts, "\n"))
	}
	landing := httpGet(t, url+"/")
	if !strings.Contains(landing, ">"+s.dir+"<") || !strings.Contains(landing, ">not sent<") || strings.Contains(landing, `href="/r/`) {
		t.Errorf("the Projects page does not list the added temp-dir project as not sent with no link:\n%s", landing)
	}
}

func TestSatelledShowsJudgeModelsOnItemPage(t *testing.T) {
	t.Parallel()
	s := newSandboxOutsideTemp(t)
	url := startSatelled(t, s)
	s.must("init")
	id := s.create("story", "--title", "judge models in satelled", "--criteria", "models appear beside verdicts").ID

	payloads := []string{
		`{"transition":"backlog->done","gate":"single-model","binding":"judge-a","model":"claude-opus-4-6","result":"accept","tokens_in":45,"tokens_out":12,"duration_ms":1300,"attempt":"att_1"}`,
		`{"transition":"backlog->done","gate":"single-unknown","binding":"judge-b","result":"reject","notes":"missing case","attempt":"att_1"}`,
		`{"transition":"backlog->done","gate":"panel-gate","binding":"judge-a,judge-b","result":"reject","tokens_in":150,"tokens_out":35,"duration_ms":2500,"attempt":"att_1","members":[{"binding":"judge-a","model":"gpt-5.4","result":"accept","tokens_in":80,"tokens_out":20,"duration_ms":1500},{"binding":"judge-b","result":"reject","tokens_in":70,"tokens_out":15,"duration_ms":1000}]}`,
		`{"transition":"backlog->done","gate":"check-gate","result":"accept","duration_ms":100,"attempt":"att_1"}`,
	}
	for _, p := range payloads {
		s.must("ledger", "append", id, "--kind", "gate.run", "--payload", p)
	}

	if out := s.must("project", "add"); !strings.Contains(out, "seeded satelled at "+url) {
		t.Fatalf("project add: %s", out)
	}
	lines := strings.Split(strings.TrimSpace(s.must("project", "partitions")), "\n")
	if len(lines) != 2 {
		t.Fatalf("partitions:\n%s", strings.Join(lines, "\n"))
	}
	key := strings.Fields(lines[1])[0]

	page := httpGet(t, url+"/r/"+key+"/story/"+id)
	vStart := strings.Index(page, "<h4>Verdicts</h4>")
	vEnd := strings.Index(page, "<h4>Attachments</h4>")
	if vStart < 0 || vEnd < vStart {
		t.Fatalf("story page lacks Verdicts section:\n%s", page)
	}
	verdicts := page[vStart:vEnd]

	ordered := []string{
		"<th>Gate</th><th>Binding</th><th>Model</th><th>Result</th>",
		"<td>single-model</td>",
		`<td class="meta">judge-a</td>`,
		`<td class="meta verdict-model">claude-opus-4-6</td>`,
		`<td><span class="verdict-result">accept</span></td>`,
		"<td>single-unknown</td>",
		`<td class="meta">judge-b</td>`,
		`<td class="meta verdict-model">unknown</td>`,
		`<td><span class="verdict-result">reject</span></td>`,
		"<td>panel-gate</td>",
		`<td class="meta">judge-a,judge-b</td>`,
		`<td class="meta verdict-model">—</td>`,
		`<td><span class="verdict-result">reject</span></td>`,
		`<tr class="verdict verdict-member v-accept">`,
		`<td class="meta">judge-a</td>`,
		`<td class="meta verdict-model">gpt-5.4</td>`,
		`<td><span class="verdict-result">accept</span></td>`,
		`<tr class="verdict verdict-member v-reject">`,
		`<td class="meta">judge-b</td>`,
		`<td class="meta verdict-model">unknown</td>`,
		`<td><span class="verdict-result">reject</span></td>`,
		"<td>check-gate</td>",
		`<td class="meta">—</td>`,
		`<td class="meta verdict-model">—</td>`,
		`<td><span class="verdict-result">accept</span></td>`,
	}
	at := 0
	for _, want := range ordered {
		i := strings.Index(verdicts[at:], want)
		if i < 0 {
			t.Errorf("Verdicts section missing %q after offset %d:\n%s", want, at, verdicts)
			break
		}
		at += i + len(want)
	}
	checkStart := strings.Index(verdicts, "<td>check-gate</td>")
	if checkStart >= 0 {
		checkEnd := strings.Index(verdicts[checkStart:], "</tr>")
		if checkEnd > 0 && strings.Contains(verdicts[checkStart:checkStart+checkEnd], "unknown") {
			t.Errorf("check-gate row showed unknown:\n%s", verdicts[checkStart:checkStart+checkEnd])
		}
	}
}
