package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// scriptSess is a live session whose replies are scripted, in order. It is the
// relay's fake peer: the same synchronous onEvent-then-channel shape a real
// transport has (see fakeSess), so the loop under test drains exactly what it
// would drain against acp/stream.
type scriptSess struct {
	replies []string
	turns   []string
	events  chan agentcli.Event
	onEvent agentcli.EventHandler
	cancels int
	closes  int
	// captured is the transport's verbatim per-turn result — what a real acp or
	// stream session hands back, and what the relay reads instead of the
	// SafeText-flattened render events. noCapture models a transport that
	// captures nothing, so the fallback path is exercised too.
	captured  []byte
	noCapture bool
}

func newScriptSess(replies ...string) *scriptSess {
	return &scriptSess{replies: replies, events: make(chan agentcli.Event, 32)}
}

func (s *scriptSess) emit(ev agentcli.Event) {
	if s.onEvent != nil {
		s.onEvent(ev)
	}
	s.events <- ev
}

func (s *scriptSess) Send(_ context.Context, turn agentcli.Turn) error {
	s.turns = append(s.turns, turn.Text)
	reply := "(script exhausted)"
	if n := len(s.turns) - 1; n < len(s.replies) {
		reply = s.replies[n]
	}
	if !s.noCapture {
		s.captured = []byte(reply)
	}
	// The render event is the LOSSY one, exactly as the transports emit it.
	s.emit(agentcli.Event{Kind: agentcli.EventMessage, Text: agentcli.SafeText(reply)})
	s.emit(agentcli.Event{Kind: agentcli.EventCompleted})
	return nil
}

func (s *scriptSess) Events() <-chan agentcli.Event { return s.events }
func (s *scriptSess) Cancel() error                 { s.cancels++; return nil }
func (s *scriptSess) Close() error                  { s.closes++; return nil }
func (s *scriptSess) Captured() []byte              { return s.captured }

func newReworkLoop(coder, consultant *scriptSess, rounds int, led *memLedger, out *bytes.Buffer) *reworkLoop {
	return &reworkLoop{
		Coder: coder, Consultant: consultant,
		CoderRole: "coder", ConsultRole: "consult",
		Rounds: rounds, Ledger: led, Out: out, Seed: "review the slice",
	}
}

// --- AC2: two sessions, relayed turns, ledgered by role ---------------------

func TestReworkRelayLedgersEveryTurnByRole(t *testing.T) {
	consultant := newScriptSess(
		"AC4 has no test.\nNOT READY: AC4 has no test",
		"Better.\nREADY",
	)
	coder := newScriptSess("added TestAC4; done")
	led := &memLedger{}
	var out bytes.Buffer

	res, err := newReworkLoop(coder, consultant, 3, led, &out).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Converged || res.Rounds != 2 {
		t.Fatalf("result = %+v; want converged after 2 rounds", res)
	}

	// The consultant speaks FIRST (it reviews the slice as it stands), then the
	// coder answers the objection, then the consultant re-checks.
	if len(consultant.turns) != 2 || consultant.turns[0] != "review the slice" {
		t.Fatalf("consultant turns = %q", consultant.turns)
	}
	if consultant.turns[1] != "added TestAC4; done" {
		t.Errorf("consultant did not receive the coder's reply verbatim: %q", consultant.turns[1])
	}
	if len(coder.turns) != 1 || coder.turns[0] != "AC4 has no test" {
		t.Fatalf("coder turns = %q; want the parsed objection", coder.turns)
	}

	// Every turn is one agent_message with its REAL direction and cc="*" — the
	// conversation reads as itself and reaches whoever judges the edge.
	type row struct{ from, to, cc string }
	var got []row
	for _, m := range led.msgs {
		got = append(got, row{m.From, m.To, m.Cc})
	}
	want := []row{
		{"consult", "coder", "*"},
		{"coder", "consult", "*"},
		{"consult", "coder", "*"},
	}
	if len(got) != len(want) {
		t.Fatalf("ledgered rows = %+v; want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if led.msgs[0].Body != "AC4 has no test.\nNOT READY: AC4 has no test" {
		t.Errorf("first row body = %q; want the consultant's whole reply", led.msgs[0].Body)
	}
}

// --- directed message: the performer acts first (sty_ec74ab22) --------------

func TestReworkRelayPerformerActsFirstOnDirectedMessage(t *testing.T) {
	coder := newScriptSess("fixed X")
	consultant := newScriptSess("fine now\nREADY")
	led := &memLedger{}
	l := newReworkLoop(coder, consultant, 3, led, &bytes.Buffer{})
	l.CoderSeed = "CODER-SEED"
	l.Pending = []string{"from orchestrator: defect X"}

	res, err := l.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Converged || res.Rounds != 1 {
		t.Fatalf("result = %+v; want converged in 1 consultant round (the opening turn is not a round)", res)
	}
	if len(coder.turns) != 1 || !strings.Contains(coder.turns[0], "CODER-SEED") || !strings.Contains(coder.turns[0], "defect X") {
		t.Fatalf("coder turns = %q; want one turn carrying the coder seed and the directed message", coder.turns)
	}
	if len(consultant.turns) != 1 || !strings.Contains(consultant.turns[0], "review the slice") || !strings.Contains(consultant.turns[0], "fixed X") {
		t.Fatalf("consultant turns = %q; want its seed plus the coder's reply", consultant.turns)
	}
	// Order of turns, read off the ledger: the coder speaks before the consultant.
	if len(led.msgs) != 2 || led.msgs[0].From != "coder" || led.msgs[1].From != "consult" {
		t.Fatalf("ledgered order = %+v; want coder then consult", led.msgs)
	}
}

func TestReworkRelayWithoutDirectedMessageStillOpensWithConsultant(t *testing.T) {
	coder := newScriptSess("done")
	consultant := newScriptSess("NOT READY: x", "READY")
	led := &memLedger{}
	l := newReworkLoop(coder, consultant, 3, led, &bytes.Buffer{})
	l.CoderSeed = "CODER-SEED"

	res, err := l.Run(context.Background())
	if err != nil || !res.Converged || res.Rounds != 2 {
		t.Fatalf("result = %+v, err %v; want converged after 2 rounds", res, err)
	}
	if len(led.msgs) == 0 || led.msgs[0].From != "consult" {
		t.Fatalf("ledgered order = %+v; want the consultant first", led.msgs)
	}
	if len(coder.turns) != 1 || !strings.HasPrefix(coder.turns[0], "CODER-SEED") {
		t.Fatalf("coder turns = %q; want the seed on its first turn", coder.turns)
	}
}

func TestReworkRelayReadyIgnoredWhileDirectedMessageUnanswered(t *testing.T) {
	// The performer returns nothing on its opening turn, so the consultant is
	// asked over an unanswered message and its READY must not end the relay.
	coder := newScriptSess("", "done")
	consultant := newScriptSess("READY", "READY")
	l := newReworkLoop(coder, consultant, 2, &memLedger{}, &bytes.Buffer{})
	l.CoderSeed = "CODER-SEED"
	l.Pending = []string{"from orchestrator: defect X"}

	res, err := l.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Converged || res.Rounds != 2 {
		t.Fatalf("result = %+v; want round 1's READY ignored and round 2's accepted", res)
	}
	if len(coder.turns) != 2 {
		t.Fatalf("coder turns = %q; want the opening turn and one resend", coder.turns)
	}
	if !strings.Contains(coder.turns[1], "defect X") || strings.Contains(coder.turns[1], "CODER-SEED") {
		t.Errorf("resend = %q; want the pending message, without a second coder seed", coder.turns[1])
	}
	if !strings.Contains(consultant.turns[1], "done") {
		t.Errorf("consultant round 2 = %q; want the coder's reply", consultant.turns[1])
	}
}

func TestReworkRelayUnansweredDirectedMessageNamedWhenBudgetSpent(t *testing.T) {
	coder := newScriptSess("", "")
	consultant := newScriptSess("READY")
	l := newReworkLoop(coder, consultant, 1, &memLedger{}, &bytes.Buffer{})
	l.Pending = []string{"from orchestrator: defect X"}

	res, err := l.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Converged {
		t.Fatalf("result = %+v; READY over an unanswered message must not converge", res)
	}
	if !strings.Contains(res.LastObjection, "unanswered directed message") || !strings.Contains(res.LastObjection, "defect X") {
		t.Errorf("last objection = %q; want it to name the unanswered message", res.LastObjection)
	}
}

// --- AC3: termination is a rule ---------------------------------------------

func TestReworkRelayStopsOnReadyMarker(t *testing.T) {
	consultant := newScriptSess("looks good\nREADY", "NOT READY: never asked")
	coder := newScriptSess("unused")
	res, err := newReworkLoop(coder, consultant, 4, &memLedger{}, &bytes.Buffer{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Converged || res.Rounds != 1 || res.LastObjection != "" {
		t.Fatalf("result = %+v; want converged on round 1 with no objection", res)
	}
	if len(coder.turns) != 0 {
		t.Errorf("coder was relayed to after READY: %q", coder.turns)
	}
}

func TestReworkRelayStopsOnBudgetAndKeepsLastObjection(t *testing.T) {
	consultant := newScriptSess(
		"NOT READY: first",
		"NOT READY: second",
		"NOT READY: third",
	)
	coder := newScriptSess("fixed 1", "fixed 2", "fixed 3")
	res, err := newReworkLoop(coder, consultant, 2, &memLedger{}, &bytes.Buffer{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Converged {
		t.Errorf("converged on a spent budget: %+v", res)
	}
	if res.Rounds != 2 {
		t.Errorf("rounds = %d, want the budget 2", res.Rounds)
	}
	if res.LastObjection != "second" {
		t.Errorf("last_objection = %q, want %q", res.LastObjection, "second")
	}
	if len(consultant.turns) != 2 {
		t.Errorf("relay spent past its budget: %d consultant turns", len(consultant.turns))
	}
}

// TestReworkRelayRejectsZeroBudget: the loop refuses rather than running
// unbounded. The parser refuses `rounds = 0` too — this is the second door.
func TestReworkRelayRejectsZeroBudget(t *testing.T) {
	l := newReworkLoop(newScriptSess(), newScriptSess(), 0, &memLedger{}, &bytes.Buffer{})
	if _, err := l.Run(context.Background()); err == nil {
		t.Fatalf("want an error for a zero budget")
	}
}

// TestReworkRelayReadsTheVerbatimTurnNotTheRender: the marker lives on the
// reply's final LINE, and both transports flatten whitespace and cap the render
// event at 240 runes. A relay that parsed the rendered text could never see a
// marker after a long reply — so it reads Captured().
func TestReworkRelayReadsTheVerbatimTurnNotTheRender(t *testing.T) {
	long := strings.Repeat("this reply is long. ", 40) // > 240 runes
	consultant := newScriptSess(long + "\nREADY")
	res, err := newReworkLoop(newScriptSess("unused"), consultant, 2, &memLedger{}, &bytes.Buffer{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Converged {
		t.Fatalf("a marker after a long reply must still be seen: %+v", res)
	}

	// A transport that captures nothing falls back to the render text, which is
	// all there is — the relay must still work, not hang.
	flat := newScriptSess("short\nREADY")
	flat.noCapture = true
	res, err = newReworkLoop(newScriptSess("unused"), flat, 2, &memLedger{}, &bytes.Buffer{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run (no capture): %v", err)
	}
	if res.Converged {
		t.Logf("flattened render happens to carry the marker; fine either way")
	}
	if res.Rounds == 0 {
		t.Errorf("the relay must still spend rounds without a capturing transport: %+v", res)
	}
}

func TestParseReadyMarker(t *testing.T) {
	for _, tc := range []struct {
		name, reply   string
		ready         bool
		wantObjection string
	}{
		{"bare ready", "READY", true, ""},
		{"ready after prose", "checked all six ACs.\n\nREADY\n", true, ""},
		{"not ready with reason", "notes\nNOT READY: AC5 is unproven", false, "AC5 is unproven"},
		{"not ready with no reason", "NOT READY:", false, "(no reason given)"},
		{
			// A consultant that cannot follow the contract must not be able to
			// hang the loop: unparseable is NOT READY, and the whole reply is
			// the objection so nothing is lost.
			"unparseable counts as not ready", "I think it is probably fine?", false, "I think it is probably fine?",
		},
		{"empty reply", "   \n\n ", false, "consultant returned no reply"},
		{
			// The marker is the FINAL non-empty line. A READY quoted mid-reply
			// is prose, not a verdict.
			"ready mentioned but not final", "you could say READY\nNOT READY: one more thing", false, "one more thing",
		},
		{"case matters", "ready", false, "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready, objection, _ := parseConsultReply(tc.reply, nil)
			if ready != tc.ready {
				t.Errorf("ready = %v, want %v", ready, tc.ready)
			}
			if objection != tc.wantObjection {
				t.Errorf("objection = %q, want %q", objection, tc.wantObjection)
			}
		})
	}
}

// --- AC4: permission policy per side ----------------------------------------

func performingSeat() seatInfo {
	return seatInfo{
		ItemID:         "sty_1",
		State:          "in_progress",
		StoryStatus:    "in_progress",
		StateAgent:     "coder",
		DispatchAgents: map[string][]string{"in_progress": {"coder"}},
		Engaged:        true,
		// A named live coder is NOT edit-capable: EditCapable is
		// Spec.IsEditCapableState, which is agent=="executor" only.
		EditCapable: false,
	}
}

// TestDispatchedPerformerPermittedNamedBinding is the test that FAILS under the
// predicate the rejected plan named. editPermitted with an empty marker takes
// the driving-session branch and asks EditCapable, which is false for every
// binding a relay can actually open (OpenSession refuses in-loop). The relay
// needs the route's own allocation instead.
func TestDispatchedPerformerPermittedNamedBinding(t *testing.T) {
	info := performingSeat()
	if editPermitted(info, dispatchMarker{}) {
		t.Fatalf("editPermitted unexpectedly allows a named live coder — this test encodes why the relay needs its own predicate")
	}
	if !dispatchedPerformerPermitted(info, "coder") {
		t.Errorf("the route allocates in_progress to coder; the relay's coder must be permitted")
	}
	if dispatchedPerformerPermitted(info, "reviewer") {
		t.Errorf("a binding the route does NOT allocate here must be refused")
	}
	if dispatchedPerformerPermitted(info, "") {
		t.Errorf("an unnamed binding must be refused")
	}

	for _, tc := range []struct {
		name  string
		mutar func(*seatInfo)
	}{
		{"stale seat", func(i *seatInfo) { i.Stale = true }},
		{"not engaged", func(i *seatInfo) { i.Engaged = false }},
		{"mid-transition", func(i *seatInfo) { i.InFlight = true }},
		{"no seat", func(i *seatInfo) { i.ItemID = "" }},
		{"another step's allocation", func(i *seatInfo) {
			i.StoryStatus = "integration"
			i.State = "integration"
			i.StateAgent = "executor"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := performingSeat()
			tc.mutar(&bad)
			if dispatchedPerformerPermitted(bad, "coder") {
				t.Errorf("%s must not permit a mutator", tc.name)
			}
		})
	}
}

// TestDispatchedPerformerPermittedAgreesWithEditCapable: for the in-loop
// executor the new branch and the old one say the same thing, which is what
// keeps the existing PreToolUse policy unchanged.
func TestDispatchedPerformerPermittedAgreesWithEditCapable(t *testing.T) {
	info := seatInfo{
		ItemID: "sty_1", State: "in_progress", StoryStatus: "in_progress",
		StateAgent: "executor", DispatchAgents: map[string][]string{"in_progress": {"executor"}},
		Engaged: true, EditCapable: true,
	}
	if !editPermitted(info, dispatchMarker{}) {
		t.Fatalf("the in-loop executor must still be permitted by editPermitted")
	}
	if !dispatchedPerformerPermitted(info, "executor") {
		t.Errorf("dispatchedPerformerPermitted disagrees with editPermitted for agent=executor")
	}
}

type decision struct {
	tool  string
	allow bool
	by    string
}

func recordDecisions(into *[]decision) func(agentcli.PermissionRequest, bool, string) {
	return func(req agentcli.PermissionRequest, allow bool, by string) {
		*into = append(*into, decision{req.ToolName, allow, by})
	}
}

func TestReworkCoderPolicyNeedsGrantAndSeat(t *testing.T) {
	const mutatorGrant = "Read,Grep,Glob,Edit,Write,Bash(go:*)"
	const readOnlyGrant = "Read,Grep,Glob,Bash(satelle:*)"
	permitted := func() (seatInfo, bool, error) { return performingSeat(), true, nil }
	denied := func() (seatInfo, bool, error) {
		i := performingSeat()
		i.InFlight = true
		return i, true, nil
	}

	for _, tc := range []struct {
		name, grant string
		seat        func() (seatInfo, bool, error)
		tool        string
		wantAllow   bool
		wantBy      string
	}{
		{"grant and seat allow the edit", mutatorGrant, permitted, "Edit", true, "policy"},
		{"grant and seat allow the shell", mutatorGrant, permitted, "Bash", true, "policy"},
		{"read is never a mutator", readOnlyGrant, permitted, "Read", true, "policy"},
		{"a read-only grant denies the edit", readOnlyGrant, permitted, "Edit", false, "grant"},
		{"a non-performing seat denies the edit", mutatorGrant, denied, "Edit", false, "policy"},
		{"no seat resolver denies the edit", mutatorGrant, nil, "Edit", false, "policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []decision
			pol := reworkCoderPolicy("coder", tc.grant, tc.seat, recordDecisions(&got))
			d := pol(agentcli.PermissionRequest{ToolName: tc.tool})
			if d.Allow != tc.wantAllow {
				t.Errorf("allow = %v, want %v", d.Allow, tc.wantAllow)
			}
			if len(got) != 1 || got[0].by != tc.wantBy || got[0].allow != tc.wantAllow {
				t.Errorf("ledgered decision = %+v; want by=%s allow=%v", got, tc.wantBy, tc.wantAllow)
			}
		})
	}
}

func TestReworkConsultPolicyDeniesMutatorsWhateverTheSeat(t *testing.T) {
	var got []decision
	pol := reworkConsultPolicy(recordDecisions(&got))
	for _, tool := range []string{"Edit", "Write", "Bash", "NotebookEdit"} {
		if pol(agentcli.PermissionRequest{ToolName: tool}).Allow {
			t.Errorf("consultant was allowed %s — a consultant that can edit is a reviewer marking its own work", tool)
		}
	}
	for _, tool := range []string{"Read", "Grep", "Glob"} {
		if !pol(agentcli.PermissionRequest{ToolName: tool}).Allow {
			t.Errorf("consultant was denied %s — reading is how it reviews", tool)
		}
	}
	if len(got) != 7 {
		t.Errorf("ledgered %d decisions, want one per ask", len(got))
	}
}

// TestGrantAllowsMutatorsMatchesTheTransports pins that the relay's grant
// clause is the transports' own classification, not a second one.
func TestGrantAllowsMutatorsMatchesTheTransports(t *testing.T) {
	if agentcli.GrantAllowsMutators("Read,Grep,Glob") {
		t.Errorf("a read-only grant must not admit mutators")
	}
	if agentcli.GrantAllowsMutators("Read,Grep,Glob,Bash(satelle:*)") {
		t.Errorf("the satelle-only pull grant must not admit mutators")
	}
	if !agentcli.GrantAllowsMutators("Read,Edit") {
		t.Errorf("an Edit grant must admit mutators")
	}
}

// --- the relay never moves status -------------------------------------------

// TestReworkLoopTouchesNoStatusVerb: the loop's only writes are message and
// invocation rows. A status change would have to go through the ledger it
// holds, and memLedger records everything it is handed.
func TestReworkLoopTouchesNoStatusVerb(t *testing.T) {
	led := &memLedger{}
	consultant := newScriptSess("NOT READY: x", "READY")
	res, err := newReworkLoop(newScriptSess("fixed"), consultant, 3, led, &bytes.Buffer{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Converged {
		t.Fatalf("result = %+v", res)
	}
	for _, m := range led.msgs {
		if strings.Contains(m.Body, "story set") || strings.Contains(m.Body, "--status") {
			t.Errorf("relay emitted a status change: %+v", m)
		}
	}
	if len(led.inv) != 0 {
		t.Errorf("loop wrote invocation rows it was not handed events for: %+v", led.inv)
	}
}
