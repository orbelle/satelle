package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/placement"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/wfroute"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func storyReworkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rework <id>",
		Short: "Run the step's bounded rework relay — a coder session and a consulting reviewer session",
		Long: `Relay the step's performer binding and its consult binding as two live
sessions until the consultant says ready or the round budget is spent. The step
opts in: rework = { consult = "reviewer", rounds = 3 }.

An unanswered directed message to the performer opens the relay performer-first;
READY cannot end it while that stands.

The consultant's reply must END with a line exactly READY or NOT READY: <reason>;
anything else consumes a round. --rounds may only LOWER the authored budget.

Turns are ledgered as agent_message rows (cc="*"). Prints {converged, outcome,
rounds, last_objection, deferred}; outcome no_progress means a round changed no
evidence and repeated the objection, so the relay stopped early.

Does NOT change status: ready is a signal, never a verdict.
See satelle help agent-dispatch.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE:        runStoryRework,
	}
	cmd.Flags().Int("rounds", 0, "lower the step's authored round budget for this run (never raises it)")
	cmd.Flags().String("model", "", "model for the coder session, overriding agents.toml (recorded with source=agent)")
	return cmd
}

// reworkPlan is what the route says about this story's rework loop: who codes,
// who consults, and how many rounds. Resolved once so the command reads as the
// three questions it asks.
type reworkPlan struct {
	CoderBinding   string
	ConsultBinding string
	Rounds         int
}

// resolveReworkPlan reads the loop off the governing route for the story's
// CURRENT status. It refuses rather than guesses: a step with no rework key has
// no loop, and inventing one would be the binary deciding process.
func resolveReworkPlan(d wfgovern.DerivedRoute, status string) (reworkPlan, error) {
	var rw reworkPlan
	for _, w := range d.Reworks {
		if w.Step == status {
			rw.ConsultBinding, rw.Rounds = w.Consult, w.Rounds
			break
		}
	}
	if rw.ConsultBinding == "" {
		return reworkPlan{}, fmt.Errorf(
			"satelle story rework: step %q declares no rework loop — author rework = { consult = \"<binding>\", rounds = N } on that step in .satelle/workflows/step.toml",
			status)
	}
	agent, known := d.Spec.StateAgent(status)
	if !known || strings.TrimSpace(agent) == "" {
		return reworkPlan{}, fmt.Errorf(
			"satelle story rework: step %q allocates no performer — there is nobody to code with", status)
	}
	rw.CoderBinding = agent
	return rw, nil
}

// refuseRemoteRework refuses a relay for a child the placement rule places
// remote (step.toml remote_agent, sty_dde8b6a4). A cloud session is one-shot and
// cannot be a relay partner, so running the relay would put remote work under a
// local coder unannounced. The way forward is the edge itself: re-presenting it
// launches a fresh cloud session carrying the reviewers' findings. The refusal
// is ledgered. A child placed local — including the not-signed-in fallback —
// passes: its performer really is local.
//
// A binding's own placement (sty_7028d996) is refused the same way: a binding
// placed remote or team runs its work off this machine, so it cannot be one side
// of a local relay either. bindings is how Decide reads it; nil reads none.
func refuseRemoteRework(ctx context.Context, d wfgovern.DerivedRoute, it workitem.Item, bindings placement.Bindings) error {
	step, ok := d.Spec.StateNamed(it.Status)
	if !ok {
		return nil
	}
	placed := placement.Decide(ctx, it, step, bindings)
	var err error
	switch {
	case placed.Team != "":
		err = fmt.Errorf(
			"satelle story rework: %s is placed team:%s (binding %q on step %q) — a team remote runs its own task and cannot be a relay partner; re-present the edge instead to wake it with the reviewers' findings",
			it.ID, placed.Team, placed.Agent, it.Status)
	case placed.Channel:
		err = fmt.Errorf(
			"satelle story rework: %s is placed remote (binding %q on step %q) — a remote performer cannot be a relay partner; re-present the edge instead to dispatch it with the reviewers' findings",
			it.ID, placed.Agent, it.Status)
	case placed.Remote:
		err = fmt.Errorf(
			"satelle story rework: %s is placed remote (remote_agent %q on step %q) — a cloud session cannot be a relay partner; re-present the edge instead to dispatch a fresh cloud session with the reviewers' findings",
			it.ID, placed.Agent, it.Status)
	default:
		return nil
	}
	_ = verb.AppendTelemetry(ctx, it.ID, "orchestrator", "rework_refused", map[string]any{
		"placement": placement.Remote, "step": it.Status, "agent": placed.Agent, "reason": err.Error(),
	})
	return err
}

func runStoryRework(cmd *cobra.Command, args []string) error {
	id := strings.TrimSpace(args[0])
	// The relay runs a consulting reviewer for minutes: an agent-facing call is
	// handed to a detached run like any other gate (sty_c4b92c9e).
	if handled, herr := handOffGate(cmd, "story-rework", id); handled {
		return herr
	}
	rounds, _ := cmd.Flags().GetInt("rounds")
	model, _ := cmd.Flags().GetString("model")
	var res reworkResult
	return runRework(cmd, id, rounds, model, &res)
}

// runRework is the relay itself, shared by `story rework` and the headless drive
// (which runs a step's declared loop between presentations of a rejected edge).
// roundsFlag lowers the authored budget (0 keeps it); modelFlag overrides the
// coder's model. The relay's answer is written to *res, and also when the relay
// ends in an error after it began, so the caller sees the rounds a failed relay
// still cost. It runs in the caller's process: hand-off is the caller's decision.
func runRework(cmd *cobra.Command, id string, roundsFlag int, modelFlag string, res *reworkResult) error {
	eng, a, err := engineForCmd(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	it, err := a.Store.Stories.Get(ctx, id)
	if err != nil {
		return err
	}
	if it.Kind != workitem.KindStory {
		return fmt.Errorf("satelle story rework: %s is not a story", id)
	}
	wfs, err := a.Store.DocIndex.List(ctx, "workflows")
	if err != nil {
		return err
	}
	d, _, err := wfgovern.RouteFor(wfs, it)
	if err != nil {
		return err
	}
	rw, err := resolveReworkPlan(d, it.Status)
	if err != nil {
		return err
	}
	if err := refuseRemoteRework(ctx, d, it, func(name string) (config.AgentBinding, bool) {
		eff, aerr := requireAgents(a)
		if aerr != nil {
			return config.AgentBinding{}, false
		}
		return eff.Agents.NamedBinding(name)
	}); err != nil {
		return err
	}
	// The budget is CONFIGURATION. --rounds is an operator saying "spend less
	// today", never "spend more than the route allows".
	if n := roundsFlag; n > 0 {
		if n > rw.Rounds {
			return fmt.Errorf("satelle story rework: --rounds %d exceeds the step's authored budget of %d — the budget is configuration; lower it in step.toml to raise it here", n, rw.Rounds)
		}
		rw.Rounds = n
	}
	eff, err := requireAgents(a)
	if err != nil {
		return err
	}
	coderBinding, found := eff.Agents.NamedBinding(rw.CoderBinding)
	if !found {
		return fmt.Errorf("satelle story rework: no [%s] binding in .satelle/workflows/agents.toml — the step allocates it as the performer", rw.CoderBinding)
	}
	if reason := agentvalidate.NotLiveCapable(rw.CoderBinding, coderBinding); reason != "" {
		if config.IsInLoopCommand(coderBinding.CommandTemplate()) {
			return fmt.Errorf("satelle story rework: %s", reason)
		}
		return fmt.Errorf("satelle story rework: %s — a one-shot performer is re-dispatched with its unanswered messages and the last rejected exit's findings by: satelle story set %s --status %s",
			reason, it.ID, it.Status)
	}
	consultBinding, found := eff.Agents.NamedBinding(rw.ConsultBinding)
	if !found {
		return fmt.Errorf("satelle story rework: no [%s] binding in .satelle/workflows/agents.toml — the step allocates it as the consultant", rw.ConsultBinding)
	}
	// Idle-stall bound for each side's turns (sty_752c4ef2), resolved the same
	// way a one-shot dispatch resolves it: binding idle_timeout= over the
	// shared [defaults] table over the shipped default.
	coderIdle, ierr := eff.Agents.ResolveIdleTimeout(coderBinding, agentstep.DefaultIdleTimeout)
	if ierr != nil {
		return fmt.Errorf("satelle story rework: invalid idle_timeout in .satelle/workflows/agents.toml [%s]: %w", rw.CoderBinding, ierr)
	}
	consultIdle, ierr := eff.Agents.ResolveIdleTimeout(consultBinding, agentstep.DefaultIdleTimeout)
	if ierr != nil {
		return fmt.Errorf("satelle story rework: invalid idle_timeout in .satelle/workflows/agents.toml [%s]: %w", rw.ConsultBinding, ierr)
	}

	// Adopt the lease's stamped session so the coder's hook and the relay
	// policy resolve the same live seat (sty_7567f047). Set only on this
	// process env — do not PublishSession an adopted lease id, or the parent
	// shell's later CLI calls would re-bind to it.
	fallbackSID := config.ResolveSession()
	var leaseRow lease.Lease
	leaseFound := false
	if a.Store.Leases != nil {
		if l, lerr := a.Store.Leases.Get(ctx, it.ID); lerr == nil {
			leaseRow, leaseFound = l, true
		} else if !errors.Is(lerr, lease.ErrNotFound) {
			return fmt.Errorf("satelle story rework: lease lookup: %w", lerr)
		}
	}
	sid := reworkSessionID(leaseRow, leaseFound, fallbackSID)
	if sid != "" {
		_ = os.Setenv(config.SessionEnv, sid)
		if sid == fallbackSID {
			config.PublishSession(sid)
		}
	}
	seat := func() (seatInfo, bool, error) { return resolveSeat(true, sid) }
	// A long relay round between edits fires no hook, so nothing else refreshes
	// the seat's heartbeat while it waits on a model (sty_7f3e6fd3).
	if leaseFound {
		defer lease.KeepAlive(ctx, a.Store.Leases, it.ID, lease.ResolveOwner())()
	}

	out := cmd.OutOrStdout()
	coderLedger := &storeSessionLedger{ctx: ctx, storyID: it.ID, ls: a.Store.Ledger, actor: rw.CoderBinding}
	consultLedger := &storeSessionLedger{ctx: ctx, storyID: it.ID, ls: a.Store.Ledger, actor: rw.ConsultBinding}

	// The coder DRIVES its own edits (executor charter, own grant, seat-checked);
	// the consultant is ASKED (consult charter, mutators refused outright). The
	// role is stated, not inferred from the binding name — a repo may name its
	// coder anything (sty_8e0b29a0). The relay marker rides only the coder
	// spawn: ACP/stream snapshot os.Environ at open, and the consultant must
	// not inherit a marker that names the coder binding.
	var coder agentcli.Session
	err = withRelayMarker(rw.CoderBinding, it.ID, func() error {
		var openErr error
		coder, openErr = reworkSessionOpener(ctx, eng, rw.CoderBinding, agentstep.SessionRoleDriving, it,
			reworkCoderPolicy(rw.CoderBinding, coderBinding.Tools, seat, invocationRecorder(coderLedger)),
			reworkEventHandler(coderLedger), modelFlag)
		return openErr
	})
	if err != nil {
		return err
	}
	defer func() { _ = coder.Close() }()

	consultant, err := reworkSessionOpener(ctx, eng, rw.ConsultBinding, agentstep.SessionRoleConsult, it,
		reworkConsultPolicy(invocationRecorder(consultLedger)),
		reworkEventHandler(consultLedger), "")
	if err != nil {
		return err
	}
	defer func() { _ = consultant.Close() }()

	fmt.Fprintf(out, "satelle story rework %s  (status %s; %s ↔ %s; up to %d round(s))\n",
		it.ID, it.Status, rw.CoderBinding, rw.ConsultBinding, rw.Rounds)

	// Both sides are seeded with the story payload — the ACs are the criteria
	// for coder, consultant and gate alike, and a live session's command
	// template need not carry {payload}.
	// The consultant additionally learns what the coder's grant can run and which
	// later gates own the evidence it cannot produce, so it can defer that
	// evidence instead of holding the relay on it.
	gates := laterGates(wfroute.Build(d.Spec, wfgovern.DerivedRouteName, it.Tags, d.Advisors, d.Reworks), it.Status)
	consultPayload, err := eng.ConsultSeed(ctx, it, rw.ConsultBinding, agentstep.ReworkContext{
		CoderBinding: rw.CoderBinding, CoderTools: coderBinding.Tools, LaterGates: gates,
	})
	if err != nil {
		return err
	}
	coderPayload, err := eng.SessionSeed(ctx, it, rw.CoderBinding)
	if err != nil {
		return err
	}
	var pending []string
	for _, m := range verb.UnansweredDirected(verb.EngagementMessages(ctx, it.ID), rw.CoderBinding) {
		pending = append(pending, fmt.Sprintf("from %s: %s", m.From, strings.TrimSpace(m.Body)))
	}
	loop := &reworkLoop{
		Coder: coder, Consultant: consultant,
		CoderRole: rw.CoderBinding, ConsultRole: rw.ConsultBinding,
		Rounds:             rw.Rounds,
		Ledger:             consultLedger,
		Out:                out,
		Seed:               consultPayload + "\n\n" + reworkSeed(rw),
		CoderSeed:          coderPayload + "\n\n" + reworkCoderSeed(rw),
		Pending:            pending,
		DeferOwners:        deferOwners(gates),
		CoderIdleTimeout:   coderIdle,
		ConsultIdleTimeout: consultIdle,
		Evidence: func(ctx context.Context) (string, error) {
			return reworkEvidenceFingerprint(ctx, a.Store.Ledger, it.ID)
		},
	}
	relay, runErr := loop.Run(ctx)
	*res = relay
	// The RESULT is recorded whether or not the relay finished cleanly: a relay
	// that died on round two still cost two rounds, and the orchestrator needs
	// to see that rather than infer it.
	recordReworkResult(ctx, a.Store.Ledger, it.ID, rw, relay)
	body, mErr := json.Marshal(relay)
	if mErr != nil {
		return mErr
	}
	fmt.Fprintf(out, "\n%s\n", body)
	if runErr != nil {
		_ = coder.Cancel()
		_ = consultant.Cancel()
		return runErr
	}
	return nil
}

// laterGates lists the reviewer gates on route steps AFTER the one holding
// status, in route order, without repeating a (step, skill) pair. These own the
// evidence a performer at status cannot produce. The route is the one the relay
// already derived; this does not derive the gate list a second time.
func laterGates(route wfroute.Route, status string) []agentstep.LaterGate {
	var out []agentstep.LaterGate
	seen := map[agentstep.LaterGate]bool{}
	after := false
	for _, st := range route.Steps {
		if !after {
			after = st.Status == status
			continue
		}
		for _, rv := range st.Reviewers {
			g := agentstep.LaterGate{Step: st.Status, Skill: rv.Skill}
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// deferOwners is the set of owners a DEFERRED line may name: the grant and the
// skill of every later gate.
func deferOwners(gates []agentstep.LaterGate) map[string]bool {
	owners := map[string]bool{agentstep.DeferOwnerGrant: true}
	for _, g := range gates {
		owners[g.Skill] = true
	}
	return owners
}

// reworkSeed is the consultant's first turn: the ask and the termination
// contract. The CONTRACT lives in prose handed to the agent, not in a Go
// branch — what Go owns is parsing the marker back (parseConsultReply).
func reworkSeed(rw reworkPlan) string {
	return fmt.Sprintf(`Review the slice as it currently stands in the working tree against this story's acceptance criteria.

You are in a bounded REWORK RELAY with the %q session, which will fix what you raise. The budget is %d round(s); this is round one.

Answer with your findings, then end your reply with a FINAL LINE that is exactly one of:

  READY
  NOT READY: <the single most important thing still wrong>

The final line is a contract, not a formality: satelle reads it to decide whether to stop relaying. Anything else counts as NOT READY and spends a round.

The payload's "rework" object states everything the coder may run ("coder_tools") and the reviewer gates that run LATER ("later_gates"). The coder cannot clear an objection whose remedy lies outside "coder_tools", and evidence a later gate owns is that gate's to produce. Do not hold the relay on either: write each one on its own line as

  DEFERRED <owner>: <the objection>

where <owner> is "grant" (the coder's tools cannot do it) or the "skill" of the later gate that owns the evidence. A deferred objection is handed to nobody and the gate receives it; end READY once only deferred objections remain. A DEFERRED line naming any other owner is an ordinary objection.

READY is not a verdict and does not advance anything. The reviewer that gates this edge runs cold and one-shot afterwards; your reply is context for it. Do not run satelle story set and do not change this story's status.`,
		rw.CoderBinding, rw.Rounds)
}

// reworkCoderSeed is the coder's first-turn briefing: what the relay is, and
// the one thing it must not do. Its charter is the executor's — it edits under
// its own grant inside the seat — so this says only what the relay adds.
func reworkCoderSeed(rw reworkPlan) string {
	return fmt.Sprintf(`You are in a bounded REWORK RELAY with the %q session, which is reviewing your slice against this story's acceptance criteria. The budget is %d round(s).

Fix what it raises, in the working tree, then reply with what you changed and why — that reply is relayed straight back to it for re-check. Stay inside the story's acceptance criteria; do not widen the slice.

Do NOT change this story's status: the relay does not advance anything. The edge is presented by the orchestrator afterwards and judged by a cold gate.`,
		rw.ConsultBinding, rw.Rounds)
}

// reworkEvidenceFingerprint is the relay's "did anything change" probe: a hash of
// the story's engagement slice (verb.StoryDiff), its attachments, and the ids of
// the ledger rows that are evidence (verb.IsEvidenceRow). It composes the
// existing owners of each; the relay's own transcript and bookkeeping rows are
// not evidence, so a round of chatter leaves it unchanged. A leg that cannot be
// read contributes a fixed marker and the others still count.
func reworkEvidenceFingerprint(ctx context.Context, ls *ledger.Store, storyID string) (string, error) {
	h := sha256.New()
	leg := func(name string, parts ...string) {
		fmt.Fprintf(h, "%s\x00", name)
		for _, p := range parts {
			fmt.Fprintf(h, "%d:%s\x00", len(p), p)
		}
	}
	if d, err := verb.StoryDiff(ctx, storyID, verb.StoryDiffOpts{Patch: true, IncludeSubstrate: true}); err == nil {
		leg("tree", strings.Join(d.Files, "\n"), d.Stat, d.Patch)
	} else {
		leg("tree", "unavailable")
	}
	if docs, err := verb.ItemDocs(ctx, storyID); err == nil {
		for _, d := range docs {
			leg("doc", d.Name, d.Body, d.SHA256)
		}
	} else {
		leg("doc", "unavailable")
	}
	if ls == nil {
		return "", fmt.Errorf("rework evidence: no ledger store")
	}
	entries, err := ls.ListByStory(ctx, storyID, "")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if verb.IsEvidenceRow(e) {
			leg("row", e.ID)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recordReworkResult appends the relay's outcome as a ledger row so the
// orchestrator can read it from the ledger as well as from stdout. Best-effort:
// a ledger write failure must not turn a completed relay into a command error.
// A no_progress stop carries its reason beside the last objection and the rounds
// used, the three things the orchestrator parks or re-presents on.
func recordReworkResult(ctx context.Context, ls *ledger.Store, storyID string, rw reworkPlan, res reworkResult) {
	if ls == nil {
		return
	}
	row := agentstep.ReworkRelayRecord{
		Relay: agentstep.ReworkRelayMarker, Converged: res.Converged, Outcome: res.Outcome, Rounds: res.Rounds,
		LastObjection: res.LastObjection, Coder: rw.CoderBinding, Consult: rw.ConsultBinding, Budget: rw.Rounds,
		Deferred: res.Deferred,
	}
	if res.Outcome == outcomeNoProgress {
		row.StoppedReason = "no new evidence and unchanged objection"
	}
	payload, err := json.Marshal(row)
	if err != nil {
		return
	}
	_, _ = ls.Append(ctx, ledger.AppendInput{
		StoryID: storyID,
		Kind:    ledger.KindAgentInvocation,
		Actor:   rw.ConsultBinding,
		Body:    fmt.Sprintf("rework outcome=%s converged=%t rounds=%d/%d deferred=%d", res.Outcome, res.Converged, res.Rounds, rw.Rounds, len(res.Deferred)),
		Payload: payload,
	}, time.Now())
}

// invocationRecorder adapts a sessionLedger to the permission-decision callback,
// so a relay's allow/deny rows land beside the chat loop's in the same shape.
func invocationRecorder(l sessionLedger) func(agentcli.PermissionRequest, bool, string) {
	return func(req agentcli.PermissionRequest, allow bool, by string) {
		if l == nil {
			return
		}
		dec := "deny"
		if allow {
			dec = "allow"
		}
		_ = l.WriteInvocation(req.ToolName, req.Kind, dec, by)
	}
}

// reworkEventHandler ledgers a session's tool boundaries. Installed as the
// request's OnEvent for the same reason the chat loop does it there: transports
// call it inline before the lossy Events() fan-out, so no boundary is lost.
func reworkEventHandler(l sessionLedger) agentcli.EventHandler {
	return func(ev agentcli.Event) {
		if l == nil {
			return
		}
		switch ev.Kind {
		case agentcli.EventToolStart:
			_ = l.WriteInvocation(ev.Tool, ev.Status, "start", "session")
		case agentcli.EventToolEnd:
			_ = l.WriteInvocation(ev.Tool, ev.Status, "end", "session")
		}
	}
}

// reworkSessionID returns the session identity the rework relay exports into
// its children. A stamped lease wins so pickSessionSeat binds the seat the
// relay is driving; an unstamped or absent lease keeps today's ResolveSession
// fallback (tree-routing still works).
func reworkSessionID(l lease.Lease, found bool, fallback string) string {
	if found {
		if id := strings.TrimSpace(l.SessionID); id != "" {
			return id
		}
	}
	return strings.TrimSpace(fallback)
}

// withRelayMarker sets SATELLE_RELAY_BINDING / SATELLE_RELAY_ITEM on the process
// environment for the duration of fn, then clears both. The rework verb wraps
// only the coder OpenSessionAs in this helper so the consultant's env snapshot
// has no marker (ACP and stream compose os.Environ at open time).
func withRelayMarker(binding, item string, fn func() error) error {
	prevBinding, hadBinding := os.LookupEnv(config.RelayBindingEnv)
	prevItem, hadItem := os.LookupEnv(config.RelayItemEnv)
	_ = os.Setenv(config.RelayBindingEnv, binding)
	_ = os.Setenv(config.RelayItemEnv, item)
	defer func() {
		if hadBinding {
			_ = os.Setenv(config.RelayBindingEnv, prevBinding)
		} else {
			_ = os.Unsetenv(config.RelayBindingEnv)
		}
		if hadItem {
			_ = os.Setenv(config.RelayItemEnv, prevItem)
		} else {
			_ = os.Unsetenv(config.RelayItemEnv)
		}
	}()
	return fn()
}

// reworkSessionOpener opens a live session for the rework relay. Tests may
// replace it to observe the process env at each open (sty_7567f047 AC3).
// modelOverride is the --model flag value for THIS open (sty_7069bced) —
// recorded with source=agent when non-empty. Only the coder open receives it;
// the consultant always opens with an empty override.
var reworkSessionOpener = func(
	ctx context.Context,
	eng *agentstep.Engine,
	binding string,
	role agentstep.SessionRole,
	it workitem.Item,
	pol agentcli.PermissionPolicy,
	onEvent agentcli.EventHandler,
	modelOverride string,
) (agentcli.Session, error) {
	return eng.OpenSessionAsWithModel(ctx, binding, role, it, pol, onEvent, modelOverride)
}
