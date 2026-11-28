package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func TestModelID(t *testing.T) {
	aliases := map[string]string{"Acme 4 Nova 300K High": "acme-4-nova-high"}
	cases := []struct{ in, want string }{
		{"Acme 4 Nova High", "acme-4-nova-high"},
		{"acme-4-nova-high", "acme-4-nova-high"},
		{"ACME_4_Nova.High", "acme-4-nova-high"},
		{"  --Acme 4  Nova High--  ", "acme-4-nova-high"},
		{"x-4.6", "x-4-6"},
		{"x-4-6", "x-4-6"},
		{"Acme 4 Nova 300K High", "acme-4-nova-high"},
		{"acme-4-nova-300k-high", "acme-4-nova-high"},
		{"", ""},
		{"  ", ""},
		{"--", ""},
	}
	for _, c := range cases {
		if got := ModelID(c.in, aliases); got != c.want {
			t.Errorf("ModelID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := ModelID("Acme 4 Nova 300K High", nil); got != "acme-4-nova-300k-high" {
		t.Errorf("ModelID without aliases = %q", got)
	}
}

func TestModelSettingsLoad(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, SettingsFile), "[models]\naliases = { \"Acme 4 Nova 300K High\" = \"Acme 4 Nova High\" }\n")
	st, err := LoadSettings(root)
	if err != nil {
		t.Fatalf("a [models] table is refused: %v", err)
	}
	if st.Models.Aliases["Acme 4 Nova 300K High"] != "Acme 4 Nova High" {
		t.Fatalf("aliases = %v", st.Models.Aliases)
	}
	for _, body := range []string{
		"[models]\naliases = { \"--\" = \"acme\" }\n",
		"[models]\naliases = { \"acme\" = \" . \" }\n",
	} {
		write(t, filepath.Join(root, SettingsFile), body)
		if _, err := LoadSettings(root); err == nil || !strings.Contains(err.Error(), "normalises to an empty identifier") {
			t.Fatalf("%q: err = %v", body, err)
		}
	}
	write(t, filepath.Join(root, SettingsFile), "[models]\nnope = 1\n")
	if _, err := LoadSettings(root); err == nil || !strings.Contains(err.Error(), "models.nope") {
		t.Fatalf("[models] is not strict: %v", err)
	}
}

type modelDispatcher struct{ text map[string]string }

func (d modelDispatcher) Dispatch(_ context.Context, _ agent.Role, req agent.Request) (agent.Result, error) {
	return agent.Result{Text: d.text[req.Binding.Name], Model: req.Binding.Model, TokensIn: 3, TokensOut: 2}, nil
}

func openModelKernel(t *testing.T) *Kernel {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(filepath.Join(root, SettingsFile))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, SettingsFile), string(settings)+"\n[models]\naliases = { \"Acme 4 Nova 300K High\" = \"acme-4-nova-high\" }\n")
	write(t, filepath.Join(root, "agents.toml"), `
[agents.sess]
command = "session"
[agents.disp]
command = "claude"
model = "Acme 4 Nova High"
tools = ["Read"]
[agents.r1]
command = "claude"
model = "Acme 4 Nova High"
tools = ["Read"]
[agents.r2]
command = "grok"
model = "Acme 4 Nova 300K High"
tools = ["Read"]
`)
	write(t, filepath.Join(root, "skills", "work.md"), "---\nname: work\n---\nDo work.\n")
	write(t, filepath.Join(root, "skills", "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, "workflows", "mflow.toml"), `
name = "mflow"
[states.st-start]
start = true
[states.st-session]
performer = { agent = "sess", skill = "work" }
[states.st-dispatched]
performer = { agent = "disp", skill = "work" }
[states.st-done]
terminal = true

[gates.panel]
skill = "review"
panel = ["r1", "r2"]

[[transition]]
from = ["st-start"]
to = "st-session"
[[transition]]
from = ["st-start"]
to = "st-dispatched"
[[transition]]
from = ["st-session"]
to = "st-done"
[[transition]]
from = ["st-dispatched"]
to = "st-done"
groups = [{ gates = ["panel"] }]
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	k.Dispatcher = modelDispatcher{text: map[string]string{
		"disp": "did it",
		"r1":   `{"verdict":"accept","notes":"ok"}`,
		"r2":   `{"verdict":"accept","notes":"ok"}`,
	}}
	return k
}

func TestModelIDRecordedAcrossHarnesses(t *testing.T) {
	k := openModelKernel(t)
	ctx := context.Background()
	k.SessionMeta = func(string) (string, string) { return "cursor", "acme-4-nova-high" }

	sess, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("s"), Workflow: ptr("mflow")}, "s-cur")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Seat(sess.ID, "s-cur", LocalLocation(k.Dir), "", "s-cur"); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"st-session", "st-done"} {
		if res, err := k.Transition(ctx, sess.ID, to, "s-cur"); err != nil || !res.Moved {
			t.Fatalf("to %s: %+v %v", to, res, err)
		}
	}

	disp, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("d"), Workflow: ptr("mflow")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(ctx, disp.ID, "st-dispatched", "me"); err != nil || !res.Moved {
		t.Fatalf("to st-dispatched: %+v %v", res, err)
	}
	if _, err := k.Perform(ctx, disp.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(ctx, disp.ID, "st-done", "me"); err != nil || !res.Moved {
		t.Fatalf("to st-done: %+v %v", res, err)
	}

	_, sruns := performerRuns(t, k, sess.ID)
	devs, druns := performerRuns(t, k, disp.ID)
	if len(sruns) != 1 || len(druns) != 1 {
		t.Fatalf("performer.run counts: session %d, dispatched %d", len(sruns), len(druns))
	}
	if sruns[0].ModelID != "acme-4-nova-high" || sruns[0].Model != "acme-4-nova-high" || sruns[0].Harness != "cursor" {
		t.Fatalf("session performer.run = %+v", sruns[0])
	}
	if druns[0].ModelID != "acme-4-nova-high" || druns[0].Model != "Acme 4 Nova High" {
		t.Fatalf("dispatched performer.run = %+v", druns[0])
	}

	runs := GateRuns(devs)
	if len(runs) != 1 || len(runs[0].Members) != 2 {
		t.Fatalf("gate.run = %+v", runs)
	}
	reported := map[string]string{}
	for _, m := range runs[0].Members {
		if m.ModelID != "acme-4-nova-high" {
			t.Fatalf("member %s model_id = %q", m.Binding, m.ModelID)
		}
		reported[m.Binding] = m.Model
	}
	if reported["r1"] != "Acme 4 Nova High" || reported["r2"] != "Acme 4 Nova 300K High" {
		t.Fatalf("members keep their spelling: %v", reported)
	}

	rows := judgeRows([][]ledger.Event{devs}, time.Time{}, time.Time{})
	if len(rows) != 1 || rows[0].Gate != "panel" || rows[0].Model != "acme-4-nova-high" || rows[0].Runs != 2 {
		t.Fatalf("judge rows = %+v", rows)
	}
	for _, row := range itemOutcome(disp, devs, true).Rows {
		if row.Model != "acme-4-nova-high" {
			t.Fatalf("outcome row model = %+v", row)
		}
	}
	if err := k.Ledger.Verify(); err != nil {
		t.Fatalf("ledger verify: %v", err)
	}
}

func TestModelIDOldEventsFoldAsBefore(t *testing.T) {
	k := openModelKernel(t)
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("old"), Workflow: ptr("mflow")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	payloads := []struct {
		kind string
		body string
	}{
		{KindPerformerRun, `{"state":"st-dispatched","binding":"disp","skill":"work","model":"Acme 4 Nova High","tokens_in":1,"tokens_out":1,"duration_ms":5}`},
		{KindPerformerRun, `{"state":"st-dispatched","binding":"disp","skill":"work","tokens_in":0,"tokens_out":0,"duration_ms":5}`},
		{gate.KindRun, `{"transition":"st-dispatched-\u003est-done","gate":"panel","result":"accept","attempt":"att_1","seq":1,"members":[{"binding":"r1","result":"accept","model":"Acme 4 Nova High","duration_ms":4},{"binding":"r2","result":"accept","model":"acme-4-nova-high","duration_ms":4}]}`},
	}
	for _, p := range payloads {
		if _, err := k.append(it.ID, p.kind, "me", json.RawMessage(p.body)); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := k.Ledger.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored []string
	for _, e := range evs {
		if e.Kind == KindPerformerRun || e.Kind == gate.KindRun {
			var c bytes.Buffer
			if err := json.Compact(&c, e.Payload); err != nil {
				t.Fatal(err)
			}
			stored = append(stored, c.String())
		}
	}
	if len(stored) != len(payloads) {
		t.Fatalf("stored %d run events, want %d", len(stored), len(payloads))
	}
	for i, p := range payloads {
		if stored[i] != p.body {
			t.Fatalf("event %d rewritten:\n got %s\nwant %s", i, stored[i], p.body)
		}
	}
	if err := k.Ledger.Verify(); err != nil {
		t.Fatalf("ledger verify: %v", err)
	}

	rows := judgeRows([][]ledger.Event{evs}, time.Time{}, time.Time{})
	if len(rows) != 2 || rows[0].Model != "Acme 4 Nova High" || rows[1].Model != "acme-4-nova-high" || rows[0].Runs != 1 || rows[1].Runs != 1 {
		t.Fatalf("old events fold differently: %+v", rows)
	}
	var models []string
	for _, row := range itemOutcome(it, evs, false).Rows {
		models = append(models, row.Model)
	}
	if got := strings.Join(models, ","); got != "Acme 4 Nova High,unknown,Acme 4 Nova High,acme-4-nova-high" {
		t.Fatalf("outcome models = %s", got)
	}
}

func TestDispatchSettingsLoad(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, SettingsFile), "[dispatch]\ntarget_env = [\"GOPATH\", \"GOCACHE\"]\n")
	st, err := LoadSettings(root)
	if err != nil || strings.Join(st.Dispatch.TargetEnv, ",") != "GOPATH,GOCACHE" {
		t.Fatalf("target_env = %v, err %v", st.Dispatch.TargetEnv, err)
	}
	write(t, filepath.Join(root, SettingsFile), "")
	if st, err := LoadSettings(root); err != nil || len(st.Dispatch.TargetEnv) != 0 {
		t.Fatalf("default target_env = %v, err %v", st.Dispatch.TargetEnv, err)
	}
	for _, name := range []string{"GH_TOKEN", "OPENAI_API_KEY", "GIT_DIR", "SATELLE_TOKEN", "A=B"} {
		write(t, filepath.Join(root, SettingsFile), "[dispatch]\ntarget_env = [\""+name+"\"]\n")
		if _, err := LoadSettings(root); err == nil || !strings.Contains(err.Error(), "[dispatch] target_env") {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}
