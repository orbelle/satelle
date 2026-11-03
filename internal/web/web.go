// Package web is satelle's push-fed read-only web UI (mirror server).
// The live verb-dispatch Server (web.New) was removed in sty_80233c10 —
// production uses NewMirror only. Shared view-model types and pure helpers
// stay here with the embedded static assets and templates.
package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/costview"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/epicset"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// serverStart marks when the web service process came up.
var serverStart = time.Now()

func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("up %ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("up %dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("up %dh %dm", h, m)
}

func globalTheme() string {
	gc, err := config.LoadGlobal()
	if err != nil {
		return ""
	}
	if gc.UI.Theme == "dark" || gc.UI.Theme == "light" {
		return gc.UI.Theme
	}
	return ""
}

func getTheme(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	t := globalTheme()
	if t == "" {
		t = "light"
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"theme": t})
}

type pageData struct {
	RepoRoot     string
	ProjectName  string
	DBPath       string
	Stories      []rowVM
	BacklogCount int
	// EngagementCount is the number of non-stale story seats (typically 0 or 1).
	// Always rendered in project chrome — 0 is visible, not hidden (sty_01ba9482).
	EngagementCount int
	// EngagedStoryIDs lists those seat item ids (sorted); empty when count is 0.
	EngagedStoryIDs []string
	Tasks           []rowVM
	Activity        []rowVM // stories and task executions under way: not backlog, not finished (sty_c356ed4f)
	DocKinds        []kindGroup
	DocCount        int
	Workflows       []workflowRowVM
	Uptime          string
	Theme           string
	TopBar          topBar
	Projects        []crumbProject // workspace project switcher for the breadcrumb
	// LastIngest is when this partition's state was last confirmed against the
	// repo, rendered as elapsed time so the page never presents an unrepaired
	// frame as current (sty_e6e467fe, presented per sty_226a661e).
	//
	// There is deliberately no Stale companion. It existed to switch a badge on
	// past mirror.StaleAfter; the elapsed time is shown unconditionally now, so
	// a boolean here would be computed and never read — which reads to the next
	// person as though a threshold still governs what is displayed.
	LastIngest time.Time
	// SyncLastSuccess / SyncReason are recorded hosted-push state (sty_30696eeb).
	SyncLastSuccess time.Time
	SyncReason      string
	SyncLocal       bool
	SyncLogPath     string
	// SyncHeld are the stories the last push held until their code is on the
	// remote (sty_c203fd62). Holding is intended while a story is in flight, so
	// they are shown apart from SyncReason, never as push failing.
	SyncHeld []syncHeldVM
	// Hosted, set when the server is the repo's store of record, links the
	// server's project page; Stories still lists every story in the local store.
	Hosted *hostedLinksVM
}

// hostedLinksVM links a hosted-mode page to the server's project page. Only
// pages the server serves are linked (sty_9ece9394).
type hostedLinksVM struct {
	Project string
}

type crumbProject struct {
	Name      string
	Slug      string
	Path      string
	Current   bool
	Ambiguous bool
}

type topBar struct {
	Uptime string
	// User is the signed-in hosted-server identity, or nil when signed out /
	// unconfigured — the account control renders an avatar+menu vs a Sign in link.
	User *topBarUser
	// Active names the current nav item so its link renders accent (DS SiteHeader):
	// "home", "projects", "help", or "" for a page no nav item represents.
	Active string
	// MirrorRO is true on the push-fed serve: no sign-in/settings forms; identity
	// (if any) comes from the pushed meta blob (epic:mirror-ui-parity order:4).
	MirrorRO bool
	// IdentityEmail is the operator email from the pushed identity meta; shown as
	// a static strip when MirrorRO is set (no auth menu).
	IdentityEmail string
}

type rowVM struct {
	workitem.Item
	// Stages are the stages the story has completed plus the one it is in, in
	// the order it reached them (PROGRESS cell).
	Stages []stageVM
	// Dispatch is the running indicator for an in-flight dispatch (sty_752c4ef2
	// AC6) — nil when nothing is in flight. Distinct from Stages: a stage
	// records PAST gate outcomes, Dispatch narrates what is happening RIGHT NOW,
	// so a refused earlier attempt's red count is never the only signal on a
	// story a live agent is currently working.
	Dispatch *dispatchVM
	// ChildrenInFlight is, for an epic-parent, how many of its children have a
	// dispatch in flight right now — the epic's roll-up of the per-story
	// indicator (sty_a42f4add). Zero for every other story.
	ChildrenInFlight int
	// Here marks a story engaged at this checkout in a hosted-mode partition. It
	// is a mark on the row, never a filter (sty_6ce071dc).
	Here bool
}

type lightPayload struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Skill string `json:"skill"`
}

type docRowVM struct {
	Name       string
	Headline   string
	ModTime    time.Time
	Provenance string // default | edited | authored; empty for free-form documents
	Source     string
}

type kindGroup struct {
	Kind string
	Docs []docRowVM
}

type seatRowVM struct {
	ID       string `json:"id"`
	InFlight bool   `json:"in_flight"`
	Stale    bool   `json:"stale"`
}

type detailData struct {
	Item   workitem.Item
	Events []eventVM
	// Route is the story's route DOCUMENT — the plan half plus every resolved
	// step's outcome, exactly as verb.recordRoute wrote it (sty_39e2d9df). Nil
	// when the story has not transitioned yet: the web layer presents this
	// artifact, it never re-derives one (sty_085e1a5a).
	Route      *storyDocVM
	Docs       []storyDocVM
	Executions []executionVM // populated only for a TASK — its runs (sty_30a917f8)
	TopBar     topBar
	Standalone bool
	// Cost is the costview headline for a STORY only (nil for a task, matching
	// the CLI's `satelle story cost` scope) — the same costview.Figures/Family
	// computation the CLI and the actual-* tags read, pre-formatted with the
	// costview formatters so the template renders, never recomputes, a figure
	// (sty_b8542a3a AC7).
	Cost *costVM
	// Members is a container's member set (id + status) — epicset's answer, the
	// same set the close guard and the other surfaces report (sty_9f4f8e12).
	// MembersNote carries the reason when an epic-parent's set cannot be fixed.
	Members     []memberVM
	MembersNote string
	// WaveSchedule and WaveError are the epic's wave as the story-wave verb
	// answered it (sty_a42f4add): the schedule it applied, or the verb's refusal
	// text. ChildrenInFlight rolls the members' running indicators up to the epic.
	WaveSchedule     string
	WaveError        string
	ChildrenInFlight int
	// Messages is a story's board: each message with its sender, addressee,
	// state and overdue flag, folded by board.Fold (sty_73c9aa10).
	Messages []messageVM
}

// memberVM is one member of a container: its id and status plus, for an
// epic-parent, what the story-wave verb reported for it and whether a dispatch is
// running on it (sty_a42f4add). Wave is "runnable" (startable now), "held", or ""
// when the verb lists the member in neither (finished) or no wave was pushed.
type memberVM struct {
	epicset.Ref
	Wave      string
	Cause     string
	Reason    string
	WaitingOn []waveBlockerVM
	Dispatch  *dispatchVM
}

// messageVM is one message on a story's Messages panel. State is the folded
// board state; Overdue is derived at render time, never stored.
type messageVM struct {
	ID       string
	From     string // sending role, with the session name when it has one
	To       string // the stored address token
	State    string
	Deadline string
	Overdue  bool
	Body     string
	Created  time.Time
}

// costVM is one story's pre-formatted cost headline plus its family roll-up
// (nil when the story has no children). Every string is already rendered
// through a costview formatter, so the template and the CLI can never
// disagree on a number's presentation.
type costVM struct {
	// Cost is the one primary dollar figure (costview.FormatDerived): the
	// measured tokens priced at read time under the operator's [pricing], or an
	// explicit unavailable. Seats is the same figure per agent+model seat, and
	// FamilySeats its epic roll-up; both are pre-formatted by costview, as the
	// CLI's BY SEAT tables are.
	Cost        string
	Seats       []costview.SeatLineView
	FamilySeats []costview.SeatLineView
	// Band is costview.FormatCostBand: low, medium, high, or unavailable — the
	// secondary size indicator after Cost.
	Band                  string
	FreshIn, EstFreshIn   string
	Out, EstOut           string
	CacheRead, CacheWrite string
	Unsplit               string
	Elapsed, EstElapsed   string
	AgentTime             string
	LegacyEstimate        string
	Family                []familyRowVM
	FamilyTotal           *familyRowVM
	// DriverRows/DriverTotal are the driving (in-loop) session's own recorded
	// usage — its own section, separate from the dispatch figures above, same
	// as the CLI's DRIVER SESSION table (sty_81caa41b, sty_b8542a3a AC4/AC7).
	// costview.FormatDriverRows is the ONE formatting pass both surfaces call,
	// so they cannot render different strings for the same ledger data. The web
	// table omits the dollar column the CLI still prints. Nil/empty when the
	// story has no driver_usage rows.
	DriverRows  []costview.DriverRowView
	DriverTotal *costview.DriverRowView
	// DriverLine says whether the driving session's own spend is in the figures
	// above; DriverAdapters is, per adapter, what its driver rows measured and
	// what they could not measure for usage (no dollar-cost clause).
	// FamilyDriverLine/FamilyAdapters say the same of the family total. All come
	// from costview's coverage formatters, as the CLI's do (sty_ca1ca935).
	DriverLine       string
	DriverAdapters   []string
	FamilyDriverLine string
	FamilyAdapters   []string
}

// familyRowVM is one family member's pre-formatted cost line — a child, or
// (as FamilyTotal) the whole family's fold.
type familyRowVM struct {
	ID string
	// Cost is the derived cost (costview.FormatDerived); Band follows it.
	Cost                  string
	Band                  string
	FreshIn, Out          string
	CacheRead, CacheWrite string
	Elapsed               string
}

type executionVM struct {
	ID        string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
	Output    string // recorded run output (frontmatter stripped); "" when none
}

type storyDocRef struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Body string `json:"body,omitempty"`
}

type storyDocVM struct {
	Name   string
	Type   string
	Anchor string // id="doc-<slug>" for hash-open from timeline links
	HTML   template.HTML
}

type chipVM struct {
	Type  string
	Label string
}

type eventVM struct {
	ledger.Entry
	Chips   []chipVM
	DocName string // set for story_doc_attached — names the document on the timeline
	DocType string
	DocHref string // relative "story/<id>#doc-<slug>" when the doc is present; else empty
}

type helpTopic struct {
	Name  string
	Title string
	HTML  template.HTML
}

type docPageData struct {
	TopBar     topBar
	Kind       string
	Name       string
	Headline   string
	HTML       template.HTML
	Provenance string
	Source     string
}

type helpPageData struct {
	Topics []helpTopic
	TopBar topBar
}

type topBarUser struct {
	Name    string
	Email   string
	Initial string
}

type settingsRowVM struct {
	FieldID  string // the config key, shown as the row's monospace id
	Label    string
	Help     string
	Value    string
	SectHead string // non-empty on the first row of a new section group
}

type settingsData struct {
	Rows     []settingsRowVM
	TopBar   topBar
	RepoRoot string
	// MirrorRO hides the global-settings link (no write surface on push-fed serve).
	MirrorRO bool
}

func settingsRows(cfg config.Config) []settingsRowVM {
	var rows []settingsRowVM
	lastSect := "\x00"
	for _, s := range config.Settings {
		vm := settingsRowVM{FieldID: s.FieldID(), Label: s.Label, Help: s.Help, Value: config.SettingDisplay(cfg, s)}
		if s.Section != lastSect {
			vm.SectHead = sectionLabel(s.Section)
			lastSect = s.Section
		}
		rows = append(rows, vm)
	}
	return rows
}

func sectionLabel(s string) string {
	if s == "" {
		return "General"
	}
	return strings.ToUpper(s[:1]) + s[1:] // "hosted" → "Hosted", "gate" → "Gate"
}

// spineDepths numbers the states on a shortest start→done path, which is what
// the status lights count. It takes a wfdot.Spec — the one lifecycle type the
// web layer knows — so no second parse or second shape exists here (sty_085e1a5a).
func spineDepths(spec wfdot.Spec) map[string]int {
	adj := map[string][]string{}
	radj := map[string][]string{}
	indeg := map[string]int{}
	for _, s := range spec.States {
		if _, ok := indeg[s.Name]; !ok {
			indeg[s.Name] = 0
		}
	}
	for _, t := range spec.Transitions {
		adj[t.From] = append(adj[t.From], t.To)
		radj[t.To] = append(radj[t.To], t.From)
		indeg[t.To]++
	}
	// Success terminal: prefer a state literally named "done"; else the first
	// terminal (no outgoing edges).
	done := ""
	for _, s := range spec.States {
		if s.Name == "done" {
			done = s.Name
			break
		}
	}
	if done == "" {
		for _, s := range spec.States {
			if len(adj[s.Name]) == 0 {
				done = s.Name
				break
			}
		}
	}
	if done == "" {
		return map[string]int{}
	}
	// Start states = no incoming edges (deterministic order).
	var starts []string
	for name, d := range indeg {
		if d == 0 {
			starts = append(starts, name)
		}
	}
	sort.Strings(starts)
	dStart := bfsDist(adj, starts)         // forward distance from the start(s)
	dDone := bfsDist(radj, []string{done}) // distance to `done` (reverse BFS)
	total, ok := dStart[done]
	if !ok {
		return map[string]int{} // start cannot reach done
	}
	// A state is a spine STEP when it lies on a shortest start→done path:
	// dStart + dDone == total. This admits the forward chain (in_progress,
	// commit_push, committed, done) and excludes both unreachable terminals
	// (cancelled) and rejoining detours (blocked) — and a back edge never lowers
	// any dStart, so the recovery loop leaves the numbering intact. The start(s)
	// (depth 0) are omitted.
	out := map[string]int{}
	for name, ds := range dStart {
		if ds < 1 {
			continue
		}
		if dd, ok := dDone[name]; ok && ds+dd == total {
			out[name] = ds
		}
	}
	return out
}

func bfsDist(adj map[string][]string, starts []string) map[string]int {
	dist := map[string]int{}
	var q []string
	for _, s := range starts {
		if _, seen := dist[s]; !seen {
			dist[s] = 0
			q = append(q, s)
		}
	}
	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		for _, m := range adj[n] {
			if _, seen := dist[m]; !seen {
				dist[m] = dist[n] + 1
				q = append(q, m)
			}
		}
	}
	return dist
}

func categoryStepOf(docs []docindex.Doc) func(category, state string) int {
	// Spine depths per category from workflow applies_to frontmatter. Prefer a
	// category-specific workflow, then wildcard (*), then the longest spine.
	// Mirrors agentstep precedence without importing agentstep (serve-binary
	// link isolation, sty_80233c10).
	var longest map[string]int
	byCat := map[string]map[string]int{}
	var wild map[string]int
	// A DERIVED route numbers a story by its own CATEGORY, so it is expanded per
	// declared category rather than per file — done.toml and step.toml are two halves
	// of one route, not two workflows (sty_9835070d).
	if rs := wfgovern.RouteSourceOf(docs); rs.Present() {
		for _, cat := range wfgovern.RouteCategories(rs.Done) {
			if _, governs, _ := wfgovern.RouteGovernsErr(docs, cat); !governs {
				continue // an authored workflow outranks the shipped route here
			}
			depths := spineDepths(routeSpecFor(docs, cat))
			if len(depths) == 0 {
				continue
			}
			if len(depths) > len(longest) {
				longest = depths
			}
			if cat == wfdotWildcard {
				if len(depths) > len(wild) {
					wild = depths
				}
				continue
			}
			if _, ok := byCat[cat]; !ok {
				byCat[cat] = depths
			}
		}
	}
	for _, d := range wfgovern.LifecycleWorkflows(docs) {
		applies := frontmatterList(d.Body, "applies_to")
		// No DOT front end (sty_d953c5d8): a workflow-kind doc that is not half of
		// the route carries no lifecycle, so it contributes no depths.
		var spec wfdot.Spec
		depths := spineDepths(spec)
		if len(depths) > len(longest) {
			longest = depths
		}
		isWild := len(applies) == 0
		for _, a := range applies {
			if a == "*" {
				isWild = true
				continue
			}
			if _, ok := byCat[a]; !ok {
				byCat[a] = depths
			}
		}
		if isWild && len(depths) > len(wild) {
			wild = depths
		}
	}
	return func(category, state string) int {
		if d, ok := byCat[category]; ok {
			return d[state]
		}
		if len(wild) > 0 {
			return wild[state]
		}
		return longest[state]
	}
}

func eventChips(e ledger.Entry) []chipVM {
	tel := ledger.EventTelemetry(e)
	var chips []chipVM
	if tel.Outcome != "" {
		chips = append(chips, chipVM{Type: "outcome", Label: tel.Outcome})
	}
	if tel.DurationMs > 0 {
		chips = append(chips, chipVM{Type: "walltime", Label: humanMs(tel.DurationMs)})
	}
	// Only show a token chip when usage was measured (sty_56aae77a). Unreported
	// must not surface as a confident 0.
	if tel.UsageAvailable && tel.TokensTotal > 0 {
		chips = append(chips, chipVM{Type: "tokens", Label: humanTokens(tel.TokensTotal) + " tok"})
	}
	// A verdict row (review_accept/review_reject) or an agent_invocation row
	// always names a model — even a legacy row with neither the alias nor the
	// resolved id renders "unknown" rather than no chip at all, matching
	// `satelle story cost`'s unconditional Model column (cmd_workitem.go)
	// (sty_87b86044). The agent-name fallback for a model-less row is gone —
	// an agent name is not a model.
	isVerdict := tel.Outcome == "accept" || tel.Outcome == "reject"
	isInvocation := e.Kind == ledger.KindAgentInvocation && !ledger.IsToolPermissionRow(e)
	if isVerdict || isInvocation {
		chips = append(chips, chipVM{Type: "model", Label: ledger.ModelLabel(tel.Model, tel.ModelResolved)})
	}
	return chips
}

func humanMs(ms int64) string {
	if ms <= 0 {
		return ""
	}
	if ms < 60000 {
		return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + "s"
	}
	return strconv.FormatInt(ms/60000, 10) + "m"
}

func humanTokens(n int) string {
	s := strconv.Itoa(n)
	if n < 1000 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func stripFrontmatter(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return strings.TrimSpace(s)
	}
	if end := strings.Index(s[4:], "\n---"); end >= 0 {
		rest := s[4+end+len("\n---"):]
		return strings.TrimSpace(strings.TrimPrefix(rest, "\n"))
	}
	return strings.TrimSpace(s)
}

// newTopBar builds RO topbar chrome (no hosted auth on the push-fed serve).
func newTopBar(active string) topBar {
	return topBar{Uptime: formatUptime(time.Since(serverStart)), Active: active}
}

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// wfdotWildcard is the done.toml section that governs every category with none of
// its own — the route-source spelling of applies_to ["*"].
const wfdotWildcard = wfdot.WildcardCategory

// routeSpecFor builds one category's derived-route Spec through the shared front
// door. An empty Spec on failure: the status lights are a read surface and must
// degrade, never refuse (sty_9835070d).
func routeSpecFor(docs []docindex.Doc, category string) wfdot.Spec {
	spec, _, _, err := wfgovern.SpecFor(docs, workitem.Item{Kind: workitem.KindStory, Category: category})
	if err != nil {
		return wfdot.Spec{}
	}
	return spec
}
