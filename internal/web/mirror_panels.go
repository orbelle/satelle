package web

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/board"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/costview"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/epicset"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// mirrorIdentity loads kind=identity id=meta for a partition.
func mirrorIdentity(ctx context.Context, s *mirror.Store, repoKey string) mirror.IdentityMeta {
	payload, err := s.GetItem(ctx, repoKey, "identity", "meta")
	if err != nil || payload == "" {
		return mirror.IdentityMeta{}
	}
	var id mirror.IdentityMeta
	_ = json.Unmarshal([]byte(payload), &id)
	return id
}

// partitionFreshness reports when this partition was last confirmed against its
// repo (sty_e6e467fe). An unknown partition returns the zero time: the page then
// says it has no confirmation at all, which is the honest reading.
//
// It no longer returns a stale BOOLEAN. The view renders elapsed time on every
// surface unconditionally (sty_226a661e), so nothing consumes a threshold —
// mirror.Partition.Stale remains for callers that genuinely branch on it.
// loadPartitionSync reads recorded hosted-push state. Missing file is empty
// (no marker, no false green). Never contacts satelle.dev.
func loadPartitionSync(repoPath string) syncstate.State {
	if strings.TrimSpace(repoPath) == "" {
		return syncstate.State{}
	}
	home := strings.TrimSpace(os.Getenv("SATELLE_HOME"))
	if home == "" {
		if testing.Testing() {
			return syncstate.State{}
		}
		home = config.GlobalDir()
	}
	st, _, err := syncstate.Load(home, repoPath)
	if err != nil {
		return syncstate.State{}
	}
	return st
}

func displayedSyncLogPath() string {
	home := strings.TrimSpace(os.Getenv("SATELLE_HOME"))
	if home == "" {
		if testing.Testing() {
			return filepath.Join(mirror.DefaultDirName, "server.log")
		}
		home = config.GlobalDir()
	}
	return mirror.ServerLogPath(home)
}

func partitionFreshness(ctx context.Context, s *mirror.Store, repoKey string) time.Time {
	p, ok, err := s.GetPartition(ctx, repoKey)
	if err != nil || !ok {
		return time.Time{}
	}
	last, _ := p.LastIngest()
	return last.Local()
}

// mirrorLoadPanels assembles pageData exclusively from mirror kinds (no repo DB).
func mirrorLoadPanels(ctx context.Context, s *mirror.Store, repoKey, slug string) (pageData, mirror.IdentityMeta, error) {
	id := mirrorIdentity(ctx, s, repoKey)

	stories, err := decodeItems(ctx, s, repoKey, "story")
	if err != nil {
		return pageData{}, id, err
	}
	here, links := hostedView(id.Hosted)
	tasks, err := decodeItems(ctx, s, repoKey, "task")
	if err != nil {
		return pageData{}, id, err
	}
	executions, err := decodeItems(ctx, s, repoKey, "execution")
	if err != nil {
		return pageData{}, id, err
	}

	docs, err := decodeDocs(ctx, s, repoKey)
	if err != nil {
		return pageData{}, id, err
	}
	byKind := map[string][]docindex.Doc{}
	prov, src := map[string]string{}, map[string]string{}
	for _, d := range docs {
		byKind[d.Kind] = append(byKind[d.Kind], d.Doc)
		key := d.Kind + "\x00" + d.Name
		if d.Provenance != "" {
			prov[key] = d.Provenance
		}
		if d.Source != "" {
			src[key] = d.Source
		}
	}
	kinds := make([]kindGroup, 0, len(config.AuthoredKinds))
	for _, k := range config.AuthoredKinds {
		dd := byKind[k]
		rows := make([]docRowVM, 0, len(dd))
		for _, d := range dd {
			key := d.Kind + "\x00" + d.Name
			rows = append(rows, docRowVM{
				Name: d.Name, Headline: d.Headline, ModTime: d.ModTime,
				Provenance: prov[key], Source: src[key],
			})
		}
		kinds = append(kinds, kindGroup{Kind: k, Docs: rows})
	}

	backlog := 0
	for _, st := range stories {
		if st.Status == workitem.StatusBacklog {
			backlog++
		}
	}

	entriesByStory, _ := decodeLedgerByStory(ctx, s, repoKey)
	liveSeat, _ := decodeLiveSeats(ctx, s, repoKey)
	engagedIDs, _ := decodeEngagedStorySeats(ctx, s, repoKey)
	dispatches, _ := decodeDispatchSeats(ctx, s, repoKey)
	gateSeats, _ := decodeGateSeats(ctx, s, repoKey)
	catStepOf := categoryStepOf(byKind["workflows"])
	specOf := categorySpecOf(byKind["workflows"])

	projectName := id.ProjectName
	if projectName == "" {
		projectName = slug
	}
	repoRoot := id.RepoRoot
	if repoRoot == "" {
		repoRoot = slug
	}

	syncSt := loadPartitionSync(repoRoot)
	logPath := ""
	if syncSt.PushReason != "" {
		logPath = displayedSyncLogPath()
	}
	storyRows := attachStagesFrom(entriesByStory, stories, liveSeat, dispatches, gateSeats, catStepOf, specOf)
	attachChildrenInFlight(storyRows, stories, dispatches)
	for i := range storyRows {
		storyRows[i].Here = here[storyRows[i].ID]
	}
	return pageData{
		RepoRoot:        repoRoot,
		ProjectName:     projectName,
		LastIngest:      partitionFreshness(ctx, s, repoKey),
		SyncLastSuccess: syncSt.PushLastSuccess,
		SyncReason:      syncSt.PushReason,
		SyncLocal:       syncSt.Scope == "local",
		SyncLogPath:     logPath,
		Stories:         storyRows,
		BacklogCount:    backlog,
		EngagementCount: len(engagedIDs),
		EngagedStoryIDs: engagedIDs,
		Tasks:           attachStagesFrom(entriesByStory, tasks, liveSeat, dispatches, gateSeats, catStepOf, specOf),
		Activity:        activityRows(storyRows, attachStagesFrom(entriesByStory, executions, liveSeat, dispatches, gateSeats, catStepOf, specOf), specOf),
		DocKinds:        kinds,
		DocCount:        len(docs),
		Workflows:       workflowRows(byKind["workflows"], prov, src),
		Uptime:          formatUptime(time.Since(serverStart)),
		Theme:           "", // client localStorage only on the mirror
		TopBar:          mirrorTopBar("", id.FooterEmail),
		Hosted:          links,
	}, id, nil
}

// hostedView is a hosted-mode partition's here-set — the stories engaged at this
// checkout, which the list marks but never filters on — and the link to the
// server's project page. Every story row in the local store is listed whatever
// engaged it (sty_6ce071dc). A local partition marks nothing and links nothing.
func hostedView(h *mirror.HostedMeta) (map[string]bool, *hostedLinksVM) {
	if h == nil || h.Server == "" || h.Project == "" {
		return nil, nil
	}
	here := make(map[string]bool, len(h.MachineStories))
	for _, id := range h.MachineStories {
		here[id] = true
	}
	return here, &hostedLinksVM{Project: strings.TrimRight(h.Server, "/") + "/app/p/" + url.PathEscape(h.Project)}
}

// activityRows is the Activity panel: the story rows and execution rows that are
// under way — out of backlog and not finished by the route that governs each
// (an execution resolves by its kind, wfgovern.WorkflowCategory) — so a running
// task execution is visible beside the stories (sty_c356ed4f).
func activityRows(stories, executions []rowVM, specOf func(category string) wfdot.Spec) []rowVM {
	out := []rowVM{}
	for _, rows := range [][]rowVM{stories, executions} {
		for _, r := range rows {
			if r.Status == workitem.StatusBacklog || finished(specOf(wfgovern.WorkflowCategory(r.Item)), r.Status) {
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

// attachStagesFrom is the mirror-side stage attach: ledger rows already loaded.
// gateSeats holds each story's gate activity while its exit gates are running.
func attachStagesFrom(entriesByStory map[string][]ledger.Entry, items []workitem.Item, liveSeat map[string]bool, dispatches map[string]*dispatchVM, gateSeats map[string]*seatGateVM, catStepOf func(category, state string) int, specOf func(category string) wfdot.Spec) []rowVM {
	out := make([]rowVM, len(items))
	for i, it := range items {
		stepOf := func(s string) int { return catStepOf(it.Category, s) }
		spec := specOf(it.Category)
		stages := buildStages(entriesByStory[it.ID], it.Status, liveSeat[it.ID], stepOf, spec, it.Tags, gateSeats[it.ID])
		stages = applyPerforming(stages, it.Status, dispatches[it.ID], stageRoute(spec, it.Tags))
		out[i] = rowVM{Item: it, Stages: stages, Dispatch: dispatches[it.ID]}
	}
	return out
}

func decodeItems(ctx context.Context, s *mirror.Store, repoKey, kind string) ([]workitem.Item, error) {
	rows, err := s.ListItems(ctx, repoKey, kind)
	if err != nil {
		return nil, err
	}
	out := make([]workitem.Item, 0, len(rows))
	for _, r := range rows {
		var it workitem.Item
		if err := json.Unmarshal([]byte(r.Payload), &it); err != nil {
			continue
		}
		if it.ID == "" {
			it.ID = r.ID
		}
		out = append(out, it)
	}
	return out, nil
}

// mirrorDoc is a doc payload with optional provenance/source from the snapshot.
type mirrorDoc struct {
	docindex.Doc
	Provenance string `json:"provenance"`
	Source     string `json:"source"`
}

func decodeDocs(ctx context.Context, s *mirror.Store, repoKey string) ([]mirrorDoc, error) {
	rows, err := s.ListItems(ctx, repoKey, "doc")
	if err != nil {
		return nil, err
	}
	out := make([]mirrorDoc, 0, len(rows))
	for _, r := range rows {
		var d mirrorDoc
		if err := json.Unmarshal([]byte(r.Payload), &d); err != nil {
			continue
		}
		if d.Name == "" {
			d.Name = r.ID
		}
		out = append(out, d)
	}
	return out, nil
}

func decodeLedgerByStory(ctx context.Context, s *mirror.Store, repoKey string) (map[string][]ledger.Entry, error) {
	rows, err := s.ListItems(ctx, repoKey, "ledger_event")
	if err != nil {
		return nil, err
	}
	out := map[string][]ledger.Entry{}
	for _, r := range rows {
		var e ledger.Entry
		if err := json.Unmarshal([]byte(r.Payload), &e); err != nil {
			continue
		}
		if e.ID == "" {
			e.ID = r.ID
		}
		if e.StoryID == "" {
			continue
		}
		out[e.StoryID] = append(out[e.StoryID], e)
	}
	// ListAll is newest-first under the export budget (sty_c5065d05);
	// ReplaceKind order is not guaranteed — sort each story's strip by CreatedAt.
	for sid, es := range out {
		ledger.SortEntries(es)
		out[sid] = es
	}
	return out, nil
}

func decodeLiveSeats(ctx context.Context, s *mirror.Store, repoKey string) (map[string]bool, error) {
	rows, err := s.ListItems(ctx, repoKey, "seat")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows {
		var seat struct {
			ID       string `json:"id"`
			InFlight bool   `json:"in_flight"`
			Stale    bool   `json:"stale"`
		}
		if err := json.Unmarshal([]byte(r.Payload), &seat); err != nil {
			continue
		}
		if seat.ID == "" {
			seat.ID = r.ID
		}
		if seat.InFlight && !seat.Stale {
			out[seat.ID] = true
		}
	}
	return out, nil
}

// seatPayload is the mirror seat row shape pushed by listSeatsJSON.
type seatPayload struct {
	ID        string `json:"id"`
	StorySeat bool   `json:"story_seat"`
	InFlight  bool   `json:"in_flight"`
	Stale     bool   `json:"stale"`
	// In-flight DISPATCH metadata (sty_752c4ef2 AC6) — omitted on the wire
	// when no dispatch has stamped detail on this seat yet.
	Agent             string    `json:"agent,omitempty"`
	Model             string    `json:"model,omitempty"`
	LastEvent         string    `json:"last_event,omitempty"`
	LastEventAt       time.Time `json:"last_event_at,omitempty"`
	ActivityStartedAt time.Time `json:"activity_started_at,omitempty"`
	IdleTimeoutNs     int64     `json:"idle_timeout_ns,omitempty"`
	// Activity is the seat's phase label; a performer dispatch stamps
	// "dispatch:<step>" (agentstep emitActivity).
	Activity string `json:"activity,omitempty"`
	// A gate round's live tally (sty_216d5205), pushed by cli.listSeatsJSON:
	// ActivityGates marks the stamp as a gate round's, ActivityTotal is its whole
	// gate count, and ActivityPassed/ActivityFailed the gates returned so far.
	ActivityGates  bool `json:"activity_gates,omitempty"`
	ActivityTotal  int  `json:"activity_total,omitempty"`
	ActivityPassed int  `json:"activity_passed,omitempty"`
	ActivityFailed int  `json:"activity_failed,omitempty"`
}

// gateActivity is the seat's running gate round, or nil when the seat is not in
// flight, is stale, or is stamped by anything but a gate round (a performer's
// dispatch runs before the gates and is not them running). A seat pushed by a
// binary that predates the stamp carries no ActivityGates and reads as nil.
func gateActivity(seat seatPayload) *seatGateVM {
	if !seat.InFlight || seat.Stale || !seat.ActivityGates || seat.ActivityTotal <= 0 {
		return nil
	}
	return &seatGateVM{Total: seat.ActivityTotal, Passed: seat.ActivityPassed, Failed: seat.ActivityFailed}
}

// decodeGateSeats builds the id → running gate round map for every seat whose
// exit gates are running.
func decodeGateSeats(ctx context.Context, s *mirror.Store, repoKey string) (map[string]*seatGateVM, error) {
	rows, err := s.ListItems(ctx, repoKey, "seat")
	if err != nil {
		return nil, err
	}
	out := map[string]*seatGateVM{}
	for _, r := range rows {
		var seat seatPayload
		if err := json.Unmarshal([]byte(r.Payload), &seat); err != nil {
			continue
		}
		if seat.ID == "" {
			seat.ID = r.ID
		}
		if g := gateActivity(seat); g != nil {
			out[seat.ID] = g
		}
	}
	return out, nil
}

// dispatchVM is the web story row's running indicator (sty_752c4ef2 AC6):
// which agent/model dispatch is in flight, how long it has run, when it last
// produced a real event, and whether idle time has crossed the warn
// threshold (half of idle_timeout) — computed server-side so the template
// stays presentation-only.
type dispatchVM struct {
	Agent       string
	Model       string
	LastEvent   string
	LastEventAt time.Time
	StartedAt   time.Time
	Warn        bool
	// Step is the workflow step the dispatched performer is working on, from
	// the seat's "dispatch:<step>" activity label; empty for any other phase
	// (a gate review, a summariser).
	Step string
}

// buildDispatchIndicator returns the running indicator for an in-flight,
// non-stale seat that has stamped agent/model detail, or nil when there is
// nothing to show (not in flight, stale, or no dispatch detail yet — a
// gate-only phase stamp with no ActivityDetail). Warn fires once idle time
// (now - LastEventAt) passes half the seat's resolved idle_timeout; a seat
// with no known idle_timeout (agents layer unresolved) never warns.
func buildDispatchIndicator(seat seatPayload, now time.Time) *dispatchVM {
	if !seat.InFlight || seat.Stale || seat.Agent == "" {
		return nil
	}
	vm := &dispatchVM{
		Agent: seat.Agent, Model: seat.Model, LastEvent: seat.LastEvent,
		LastEventAt: seat.LastEventAt, StartedAt: seat.ActivityStartedAt,
	}
	if step, ok := strings.CutPrefix(seat.Activity, "dispatch:"); ok {
		vm.Step = step
	}
	if seat.IdleTimeoutNs > 0 && !seat.LastEventAt.IsZero() {
		idle := now.Sub(seat.LastEventAt)
		vm.Warn = idle >= time.Duration(seat.IdleTimeoutNs)/2
	}
	return vm
}

// decodeDispatchSeats builds the id → running-indicator map for every
// in-flight, non-stale seat carrying dispatch detail (sty_752c4ef2 AC6).
func decodeDispatchSeats(ctx context.Context, s *mirror.Store, repoKey string) (map[string]*dispatchVM, error) {
	rows, err := s.ListItems(ctx, repoKey, "seat")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := map[string]*dispatchVM{}
	for _, r := range rows {
		var seat seatPayload
		if err := json.Unmarshal([]byte(r.Payload), &seat); err != nil {
			continue
		}
		if seat.ID == "" {
			seat.ID = r.ID
		}
		if vm := buildDispatchIndicator(seat, now); vm != nil {
			out[seat.ID] = vm
		}
	}
	return out, nil
}

// engagedStorySeatIDs returns non-stale story seats (story_seat && !stale).
// Distinct from decodeLiveSeats (in_flight && !stale) used only for progress
// lights — a settled engaged lease still counts as engaged (sty_01ba9482).
func engagedStorySeatIDs(seats []seatPayload) []string {
	var ids []string
	for _, seat := range seats {
		if !seat.StorySeat || seat.Stale {
			continue
		}
		id := strings.TrimSpace(seat.ID)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// decodeEngagedStorySeats lists non-stale story-seat holders for engagement chrome.
func decodeEngagedStorySeats(ctx context.Context, s *mirror.Store, repoKey string) ([]string, error) {
	rows, err := s.ListItems(ctx, repoKey, "seat")
	if err != nil {
		return nil, err
	}
	seats := make([]seatPayload, 0, len(rows))
	for _, r := range rows {
		var seat seatPayload
		if err := json.Unmarshal([]byte(r.Payload), &seat); err != nil {
			continue
		}
		if seat.ID == "" {
			seat.ID = r.ID
		}
		seats = append(seats, seat)
	}
	return engagedStorySeatIDs(seats), nil
}

// mirrorLoadDetail builds the expand/detail VM from mirror kinds only.
func mirrorLoadDetail(ctx context.Context, s *mirror.Store, repoKey, group, id string) (detailData, mirror.IdentityMeta, error) {
	// group is the mirror kind: story, task, or a task execution (whose ledger
	// carries its run's stages, sty_c356ed4f). Cost, messages and members are a
	// story's alone.
	payload, err := s.GetItem(ctx, repoKey, group, id)
	if err != nil {
		return detailData{}, mirror.IdentityMeta{}, err
	}
	var item workitem.Item
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return detailData{}, mirror.IdentityMeta{}, err
	}
	if item.ID == "" {
		item.ID = id
	}

	// Story docs from mirror story_doc kind (id = story_id/name). The route
	// document is lifted out of the list into its own section — it is the story's
	// route and the reasoning behind every outcome, not one attachment among many.
	// Built before the timeline so story_doc_attached events can resolve hrefs.
	var docs []storyDocVM
	var route *storyDocVM
	presentDocs := map[string]bool{}
	if sdocs, err := s.ListItems(ctx, repoKey, "story_doc"); err == nil {
		prefix := id + "/"
		for _, r := range sdocs {
			if !strings.HasPrefix(r.ID, prefix) && !strings.HasPrefix(r.ID, id+"_") {
				// accept id = "storyID/name" form
				if !strings.HasPrefix(r.ID, id) {
					continue
				}
			}
			var ref struct {
				Name string `json:"name"`
				Type string `json:"type"`
				Body string `json:"body"`
			}
			_ = json.Unmarshal([]byte(r.Payload), &ref)
			name := ref.Name
			if name == "" {
				name = filepath.Base(r.ID)
			}
			if group == "task" && (strings.HasPrefix(name, "exe_") || strings.HasPrefix(name, "output-")) {
				continue
			}
			vm := storyDocVM{
				Name: name, Type: ref.Type, Anchor: docAnchor(name),
				HTML: renderMarkdown(ref.Body),
			}
			if name == routeDocName {
				route = &vm
				presentDocs[routeDocName] = true
				continue
			}
			docs = append(docs, vm)
			presentDocs[name] = true
		}
	}

	entriesByStory, _ := decodeLedgerByStory(ctx, s, repoKey)

	// Cost headline — a STORY only (nil for a task, matching the CLI's
	// `satelle story cost` scope), computed via costview BEFORE the timeline
	// reversal below mutates entriesByStory[id] in place (sty_b8542a3a AC7).
	var cost *costVM
	if group == "story" {
		cost = mirrorBuildCostVM(ctx, s, repoKey, item, entriesByStory)
	}

	// Before the timeline reversal below, which reorders this slice in place.
	var messages []messageVM
	if group == "story" {
		messages = buildMessageVMs(entriesByStory[id], time.Now())
	}

	events := entriesByStory[id]
	// Reverse for timeline (newest first) — same as loadDetail.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	kindPath := group
	evs := make([]eventVM, len(events))
	for i, e := range events {
		ev := eventVM{Entry: e, Chips: eventChips(e)}
		if e.Kind == "story_doc_attached" {
			name, typ := attachedDocRef(e)
			ev.DocName = name
			ev.DocType = typ
			if name != "" && presentDocs[name] {
				ev.DocHref = kindPath + "/" + item.ID + "#" + docAnchor(name)
			}
		}
		evs[i] = ev
	}

	var executions []executionVM
	if group == "task" {
		if runs, err := decodeItems(ctx, s, repoKey, "execution"); err == nil {
			for _, run := range runs {
				if run.ParentID != id {
					continue
				}
				executions = append(executions, executionVM{
					ID: run.ID, Status: run.Status, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
				})
			}
		}
	}

	var members []memberVM
	var membersNote string
	var wave *waveVM
	childrenInFlight := 0
	if group == "story" {
		var refs []epicset.Ref
		refs, membersNote = mirrorBuildMembers(ctx, s, repoKey, item)
		dispatches, _ := decodeDispatchSeats(ctx, s, repoKey)
		wave = mirrorWave(ctx, s, repoKey, item)
		members = annotateMembers(refs, wave, dispatches)
		childrenInFlight = rollupDispatch(refs, dispatches)
	}
	waveSchedule, waveError := "", ""
	if wave != nil {
		waveSchedule, waveError = wave.Schedule, wave.Error
	}

	idMeta := mirrorIdentity(ctx, s, repoKey)
	return detailData{
		Item: item, Events: evs, Route: route, Docs: docs, Executions: executions,
		TopBar: mirrorTopBar("", idMeta.FooterEmail), Cost: cost,
		Members: members, MembersNote: membersNote, Messages: messages,
		WaveSchedule: waveSchedule, WaveError: waveError, ChildrenInFlight: childrenInFlight,
	}, idMeta, nil
}

// buildMessageVMs is the Messages panel of one story's mirrored ledger rows:
// board.Fold's answer, oldest first, with the overdue flag at now. The relay's
// own transcript rows (cc "*") are conversation, not messages anyone awaits, and
// are left out as on the CLI board.
func buildMessageVMs(entries []ledger.Entry, now time.Time) []messageVM {
	var msgs, marks []ledger.Entry
	for _, e := range entries {
		switch e.Kind {
		case ledger.KindAgentMessage:
			if !board.Transcript(e) {
				msgs = append(msgs, e)
			}
		case ledger.KindMessageMark:
			marks = append(marks, e)
		}
	}
	var out []messageVM
	for _, m := range board.Fold(msgs, marks) {
		vm := messageVM{
			ID: m.ID, From: m.From, To: m.To, State: string(m.State),
			Overdue: board.Overdue(m, now), Body: m.Body, Created: m.CreatedAt,
		}
		if m.FromSessionName != "" {
			vm.From += " (" + m.FromSessionName + ")"
		}
		if m.Deadline != nil {
			vm.Deadline = m.Deadline.UTC().Format(time.RFC3339)
		}
		out = append(out, vm)
	}
	return out
}

// mirrorBuildMembers is the container's member set for the story view
// (sty_9f4f8e12) — epicset's answer over the mirror's indexed stories, the same
// set the close guard, the close-gate payload, the CLI route view and the
// workspace aggregate report. The note is non-empty when an epic-parent's set
// cannot be fixed (the close refuses it); a story that is not a container
// returns neither members nor a note.
func mirrorBuildMembers(ctx context.Context, s *mirror.Store, repoKey string, item workitem.Item) ([]epicset.Ref, string) {
	stories, err := decodeItems(ctx, s, repoKey, "story")
	if err != nil {
		return nil, ""
	}
	members, err := epicset.MembersFromItems(stories, item)
	if err != nil {
		return nil, err.Error()
	}
	return epicset.Refs(members), ""
}

// waveVM is the web-local shape of the story-wave verb's JSON response as the CLI
// pushed it (sty_a42f4add) — the same keys as verb.WaveResult, plus the refusal
// text a refused verb is pushed as. The web renders it and never recomputes it;
// it imports no verb, as the cost view below.
type waveVM struct {
	Schedule string          `json:"schedule"`
	Runnable []string        `json:"runnable"`
	Omitted  []waveOmittedVM `json:"omitted"`
	Error    string          `json:"error"`
}

type waveOmittedVM struct {
	ID        string          `json:"id"`
	Cause     string          `json:"cause"`
	Reason    string          `json:"reason"`
	WaitingOn []waveBlockerVM `json:"waiting_on"`
}

type waveBlockerVM struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Cancelled bool   `json:"cancelled"`
}

// mirrorWave reads the pushed wave row of an epic-parent; nil when the item is not
// one, or no wave was pushed for it.
func mirrorWave(ctx context.Context, s *mirror.Store, repoKey string, item workitem.Item) *waveVM {
	if !epicset.IsEpicParent(item) {
		return nil
	}
	payload, err := s.GetItem(ctx, repoKey, "wave", item.ID)
	if err != nil {
		return nil
	}
	var w waveVM
	if json.Unmarshal([]byte(payload), &w) != nil {
		return nil
	}
	return &w
}

// annotateMembers joins what the verb said (wave) and which dispatches run onto
// the container's member set. The member set stays epicset's; wave only
// annotates it, and a nil wave or a refusal annotates nothing.
func annotateMembers(refs []epicset.Ref, wave *waveVM, dispatches map[string]*dispatchVM) []memberVM {
	runnable := map[string]bool{}
	omitted := map[string]waveOmittedVM{}
	if wave != nil && wave.Error == "" {
		for _, id := range wave.Runnable {
			runnable[id] = true
		}
		for _, o := range wave.Omitted {
			omitted[o.ID] = o
		}
	}
	out := make([]memberVM, 0, len(refs))
	for _, r := range refs {
		m := memberVM{Ref: r, Dispatch: dispatches[r.ID]}
		if runnable[r.ID] {
			m.Wave = "runnable"
		} else if o, ok := omitted[r.ID]; ok {
			m.Wave, m.Cause, m.Reason, m.WaitingOn = "held", o.Cause, o.Reason, o.WaitingOn
		}
		out = append(out, m)
	}
	return out
}

// rollupDispatch counts the members with a dispatch in flight — the epic's
// roll-up of the per-story running indicator.
func rollupDispatch(members []epicset.Ref, dispatches map[string]*dispatchVM) int {
	n := 0
	for _, m := range members {
		if dispatches[m.ID] != nil {
			n++
		}
	}
	return n
}

// attachChildrenInFlight sets ChildrenInFlight on every epic-parent row of rows,
// over the member set epicset reports for it.
func attachChildrenInFlight(rows []rowVM, stories []workitem.Item, dispatches map[string]*dispatchVM) {
	for i := range rows {
		if !epicset.IsEpicParent(rows[i].Item) {
			continue
		}
		members, err := epicset.MembersFromItems(stories, rows[i].Item)
		if err != nil {
			continue
		}
		rows[i].ChildrenInFlight = rollupDispatch(epicset.Refs(members), dispatches)
	}
}

// mirrorBuildCostVM computes item's costview headline plus its family
// roll-up (nil when item has no children) — the SAME costview.Own/
// costview.Family computation ComputeStoryActual and `satelle story cost` use,
// with the workflow's Clock resolved through the one front door,
// wfgovern.ClockFor, off the mirror's own indexed workflow docs so the web
// page needs no link to verb or serve.
func mirrorBuildCostVM(ctx context.Context, s *mirror.Store, repoKey string, item workitem.Item, entriesByStory map[string][]ledger.Entry) *costVM {
	docs, err := decodeDocs(ctx, s, repoKey)
	if err != nil {
		return nil
	}
	var workflows []docindex.Doc
	for _, d := range docs {
		if d.Kind == "workflows" {
			workflows = append(workflows, d.Doc)
		}
	}
	clockFor := func(it workitem.Item) costview.Clock {
		clk, _ := wfgovern.ClockFor(workflows, it)
		return clk
	}

	now := time.Now()
	// Priced now, from the operator's current [pricing]: changing it re-prices
	// every story on its next render.
	pricing := costview.WithPricing(config.LoadPricing())
	own := costview.Own(item, entriesByStory[item.ID], clockFor(item), now, pricing)

	stories, err := decodeItems(ctx, s, repoKey, "story")
	if err != nil {
		return costVMFromStory(own, nil)
	}
	hasChildren := false
	for _, it := range stories {
		if it.ParentID == item.ID {
			hasChildren = true
			break
		}
	}
	if !hasChildren {
		return costVMFromStory(own, nil)
	}
	fam := costview.Family(item, stories, entriesByStory, clockFor, now, pricing)
	return costVMFromStory(own, &fam)
}

// costVMFromStory renders one item's costview.Story (plus its family roll-up,
// when non-nil) into the pre-formatted costVM the template reads. It performs
// no I/O — every figure comes through a costview formatter, the SAME ones the
// CLI's printCostSummary calls, so a shared fixture produces byte-identical
// strings on both surfaces (sty_b8542a3a AC7).
func costVMFromStory(own costview.Story, fam *costview.FamilyCost) *costVM {
	splitRows := own.Figures.UsageRows - own.Figures.UnsplitRows
	vm := &costVM{
		Cost:           costview.FormatDerived(own.Figures.Derived),
		Seats:          costview.FormatSeatLines(own.Seats),
		Band:           costview.FormatCostBand(own.Figures),
		FreshIn:        costview.FormatSplitTokens(own.Figures.FreshInput, splitRows, own.Figures.UnsplitRows, own.Figures.UsageUnavailableRows),
		EstFreshIn:     costview.FormatEstimate(own.Estimates, "fresh-input"),
		Out:            costview.FormatTokensMeasured(own.Figures.Output, own.Figures.UsageRows, own.Figures.UsageUnavailableRows),
		EstOut:         costview.FormatEstimate(own.Estimates, "output"),
		CacheRead:      costview.FormatSplitTokens(own.Figures.CacheRead, splitRows, own.Figures.UnsplitRows, own.Figures.UsageUnavailableRows),
		CacheWrite:     costview.FormatSplitTokens(own.Figures.CacheWrite, splitRows, own.Figures.UnsplitRows, own.Figures.UsageUnavailableRows),
		Elapsed:        costview.FormatDuration(own.Figures.ElapsedMs),
		EstElapsed:     costview.FormatEstimate(own.Estimates, "minutes"),
		AgentTime:      costview.FormatDuration(own.Figures.AgentMs),
		LegacyEstimate: costview.FormatLegacyTokenEstimate(own.Estimates),
	}
	if own.Figures.UnsplitTokens > 0 {
		vm.Unsplit = costview.FormatTokens(own.Figures.UnsplitTokens)
	}
	vm.DriverRows, vm.DriverTotal = costview.FormatDriverRows(own.DriverRows)
	vm.DriverLine = costview.FormatDriverCoverage(own.Figures)
	vm.DriverAdapters = costview.FormatAdapterUsageCoverage(own.Figures)
	if fam == nil {
		return vm
	}
	vm.FamilySeats = costview.FormatSeatLines(fam.Seats)
	vm.FamilyDriverLine = costview.FormatDriverCoverage(fam.Total)
	vm.FamilyAdapters = costview.FormatAdapterUsageCoverage(fam.Total)
	for _, c := range fam.Children {
		vm.Family = append(vm.Family, familyRowFromFigures(c.ID, c.Figures))
	}
	total := familyRowFromFigures("FAMILY TOTAL", fam.Total)
	vm.FamilyTotal = &total
	return vm
}

func familyRowFromFigures(id string, f costview.Figures) familyRowVM {
	splitRows := f.UsageRows - f.UnsplitRows
	return familyRowVM{
		ID:         id,
		Cost:       costview.FormatDerived(f.Derived),
		Band:       costview.FormatCostBand(f),
		FreshIn:    costview.FormatSplitTokens(f.FreshInput, splitRows, f.UnsplitRows, f.UsageUnavailableRows),
		Out:        costview.FormatTokensMeasured(f.Output, f.UsageRows, f.UsageUnavailableRows),
		CacheRead:  costview.FormatSplitTokens(f.CacheRead, splitRows, f.UnsplitRows, f.UsageUnavailableRows),
		CacheWrite: costview.FormatSplitTokens(f.CacheWrite, splitRows, f.UnsplitRows, f.UsageUnavailableRows),
		Elapsed:    costview.FormatDuration(f.ElapsedMs),
	}
}

// routeDocName is the name verb.RouteDocName writes the route document under.
// Duplicated rather than imported: internal/serve must link neither verb nor the
// rest of the repo-writing stack (internal/serve/deps_test.go).
const routeDocName = "route"

// docAnchor returns the fragment id for a story document (doc-<slug>).
func docAnchor(name string) string {
	var b strings.Builder
	b.WriteString("doc-")
	prevDash := true // suppress leading dashes
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	s := b.String()
	return strings.TrimRight(s, "-")
}

// attachedQuotedNameRe finds quoted runs in a story_doc_attached body
// (`attached plan document "plan"` / binary bodies with the filename quoted).
var attachedQuotedNameRe = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)

// attachedDocRef resolves the document name/type for a story_doc_attached
// ledger entry: prefer the structured payload, fall back to parsing the body
// so pre-payload ledger rows still render a named timeline item.
func attachedDocRef(e ledger.Entry) (name, typ string) {
	if len(e.Payload) > 0 && string(e.Payload) != "{}" {
		var p struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if json.Unmarshal(e.Payload, &p) == nil {
			name, typ = p.Name, p.Type
		}
	}
	if name == "" {
		if m := attachedQuotedNameRe.FindAllString(e.Body, -1); len(m) > 0 {
			if u, err := strconv.Unquote(m[len(m)-1]); err == nil {
				name = u
			}
		}
	}
	if typ == "" {
		// "attached <type> document|binary \"…\""
		body := strings.TrimSpace(e.Body)
		if strings.HasPrefix(body, "attached ") {
			rest := strings.TrimPrefix(body, "attached ")
			fields := strings.Fields(rest)
			if len(fields) >= 2 && (fields[1] == "document" || fields[1] == "binary") {
				typ = fields[0]
			}
		}
	}
	if name == "" {
		name = "(unknown)"
	}
	return name, typ
}

// mirrorTopBar builds chrome for the push-fed surface: no auth forms; optional
// identity email from the pushed meta blob (order:4).
func mirrorTopBar(active, identityEmail string) topBar {
	tb := newTopBar(active)
	tb.User = nil
	tb.IdentityEmail = identityEmail
	tb.MirrorRO = true
	return tb
}

// mirrorSettingsData loads the pushed settings blob into the settings template VM.
func mirrorSettingsData(ctx context.Context, s *mirror.Store, repoKey string) (settingsData, mirror.IdentityMeta, error) {
	id := mirrorIdentity(ctx, s, repoKey)
	payload, err := s.GetItem(ctx, repoKey, "settings", "config")
	if err != nil || payload == "" {
		return settingsData{
			TopBar:   mirrorTopBar("", id.FooterEmail),
			RepoRoot: id.RepoRoot,
			Rows:     nil,
			MirrorRO: true,
		}, id, nil
	}
	var blob struct {
		RepoRoot string `json:"repo_root"`
		Rows     []struct {
			FieldID string `json:"field_id"`
			Label   string `json:"label"`
			Help    string `json:"help"`
			Value   string `json:"value"`
			Section string `json:"section"`
		} `json:"rows"`
	}
	_ = json.Unmarshal([]byte(payload), &blob)
	lastSect := "\x00"
	var rows []settingsRowVM
	for _, r := range blob.Rows {
		vm := settingsRowVM{FieldID: r.FieldID, Label: r.Label, Help: r.Help, Value: r.Value}
		sect := r.Section
		if sect != lastSect {
			vm.SectHead = sectionLabel(sect)
			lastSect = sect
		}
		rows = append(rows, vm)
	}
	root := blob.RepoRoot
	if root == "" {
		root = id.RepoRoot
	}
	return settingsData{
		Rows:     rows,
		TopBar:   mirrorTopBar("", id.FooterEmail),
		RepoRoot: root,
		MirrorRO: true,
	}, id, nil
}

// findDoc returns one mirrored doc by kind+name.
func findDoc(ctx context.Context, s *mirror.Store, repoKey, kind, name string) (mirrorDoc, error) {
	docs, err := decodeDocs(ctx, s, repoKey)
	if err != nil {
		return mirrorDoc{}, err
	}
	for _, d := range docs {
		if d.Kind == kind && d.Name == name {
			return d, nil
		}
	}
	return mirrorDoc{}, errNotFound
}

// errNotFound is a sentinel for missing mirror rows.
var errNotFound = errString("not found")

type errString string

func (e errString) Error() string { return string(e) }
