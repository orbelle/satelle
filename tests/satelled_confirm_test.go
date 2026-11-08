//go:build integration

package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// confirmSandbox runs satelled outside any agent session and a repo whose
// story reaches confirm, the operator's state (type confirmation), gateless
// for the test; confirm moves on to approved, which enqueues into the ship
// queue, or back to in_progress. It returns the sandbox, satelled's URL, the
// partition key and a story at confirm carrying a brief.
func confirmSandbox(t *testing.T) (*sandbox, string, string, string) {
	t.Helper()
	s := newSandbox(t)
	base := startSatelled(t, s)
	s.must("init")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[[workflows.rule]]\nworkflow = \"runbook\"\nkind = \"execution\"\n")
	s.write(".satelle/workflows/runbook.toml", "name = \"runbook\"\n[states.backlog]\nstart = true\n[states.done]\nterminal = true\n[[transition]]\nfrom = [\"backlog\"]\nto = \"done\"\n")
	s.write(".satelle/queues/ship.toml", "title = \"ship\"\n[trigger]\nworkflow = \"runbook\"\nmin_size = 0\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.backlog]
start = true
[states.in_progress]
[states.confirm]
performer = { operator = "confirmation" }
[states.approved]
enqueue = ["ship"]
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "confirm"
[[transition]]
from = ["confirm"]
to = "approved"
[[transition]]
from = ["confirm"]
to = "in_progress"
[[transition]]
from = ["approved"]
to = "done"
`)
	s.must("validate")
	s.must("project", "add")
	lines := strings.Split(strings.TrimSpace(s.must("project", "partitions")), "\n")
	if len(lines) != 2 {
		t.Fatalf("partitions:\n%s", strings.Join(lines, "\n"))
	}
	key := strings.Fields(lines[1])[0]
	id := s.create("story", "--title", "confirm me").ID
	s.must("story", "set", id, "--status", "in_progress")
	s.must("story", "set", id, "--status", "confirm")
	if r := s.exec(satelleBin, "## Changes\n\nthe brief body\n", "story", "attach", id, "--name", "result-summary", "--type", "text/markdown; type=brief", "--file", "-"); r.code != 0 {
		t.Fatalf("attach brief: %s%s", r.out, r.err)
	}
	return s, base, key, id
}

// postOperatorWord posts satelled's Awaiting-you form as the browser does.
func postOperatorWord(t *testing.T, base, key, id string, form url.Values) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/r/"+key+"/operator-record/"+id, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// operatorRecords is the item's operator-marked attachment.added payloads.
func operatorRecords(s *sandbox, id string) []map[string]any {
	var out []map[string]any
	for _, e := range s.ledger(id, "attachment.added") {
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			s.t.Fatal(err)
		}
		if p["operator"] == true {
			out = append(out, p)
		}
	}
	return out
}

// AC5, AC6: satelled lists the story at confirm with its brief, records the
// operator's word from the page's form, and the driving session then presents
// exactly the move it names, which enqueues the confirmed story.
func TestSatelledConfirmsAStory(t *testing.T) {
	t.Parallel()
	s, base, key, id := confirmSandbox(t)
	page := httpGet(t, base+"/r/"+key+"/operator")
	for _, want := range []string{"confirm me", "the brief body", "awaiting your word", `value="approved"`, `value="in_progress"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Awaiting-you page lacks %q", want)
		}
	}
	code, body := postOperatorWord(t, base, key, id, url.Values{"to": {"approved"}, "word": {"tried it, works"}, "retype": {id}})
	if code != http.StatusSeeOther {
		t.Fatalf("POST: %d %s", code, body)
	}
	recs := operatorRecords(s, id)
	if len(recs) != 1 || recs[0]["to"] != "approved" || recs[0]["name"] != "confirmation" {
		t.Fatalf("operator records: %+v", recs)
	}
	var evs []struct {
		Actor string `json:"actor"`
		Kind  string `json:"kind"`
	}
	s.mustJSON(&evs, "ledger", "list", id, "--kind", "attachment.added")
	if evs[len(evs)-1].Actor != "tester" {
		t.Errorf("recorded as %q, want the OS user", evs[len(evs)-1].Actor)
	}
	if !strings.Contains(httpGet(t, base+"/r/"+key+"/operator"), "tester chose approved") {
		t.Error("the page does not show the recorded word")
	}
	s.env["SATELLE_SESSION"] = "driver"
	if r := s.run("story", "set", id, "--status", "in_progress"); r.code == 0 || !strings.Contains(r.out+r.err, "names the move to approved") {
		t.Fatalf("the unnamed move: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if out := s.must("story", "set", id, "--status", "approved"); !strings.Contains(out, "ship") {
		t.Fatalf("confirm -> approved did not enqueue:\n%s", out)
	}
	if st := s.get(id).Status; st != "approved" {
		t.Fatalf("status %s", st)
	}
}

// AC4, AC6: a send-back from satelled records the reason, and the driving
// session's move reopens the story with the reason attached for the coder.
func TestSatelledSendsBack(t *testing.T) {
	t.Parallel()
	s, base, key, id := confirmSandbox(t)
	const reason = "the how-to fails at step 2"
	if code, body := postOperatorWord(t, base, key, id, url.Values{"to": {"in_progress"}, "word": {""}, "retype": {id}}); code != http.StatusConflict || !strings.Contains(body, "is empty") {
		t.Fatalf("an empty send-back: %d %s", code, body)
	}
	if code, body := postOperatorWord(t, base, key, id, url.Values{"to": {"in_progress"}, "word": {reason}, "retype": {id}}); code != http.StatusSeeOther {
		t.Fatalf("POST: %d %s", code, body)
	}
	s.env["SATELLE_SESSION"] = "driver"
	if r := s.run("story", "set", id, "--status", "approved"); r.code == 0 {
		t.Fatal("approved despite a send-back")
	}
	s.must("story", "set", id, "--status", "in_progress")
	if got := s.must("story", "attachment", id, "confirmation"); got != reason {
		t.Fatalf("confirmation body %q", got)
	}
}
