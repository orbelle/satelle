package web

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/seatlife"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
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

// syncHeldVM is one story the last push held until its code is on the remote.
type syncHeldVM struct {
	ID     string
	Reason string
}

// syncHeldRows orders the recorded held stories by id for the header.
func syncHeldRows(held map[string]string) []syncHeldVM {
	if len(held) == 0 {
		return nil
	}
	rows := make([]syncHeldVM, 0, len(held))
	for _, id := range slices.Sorted(maps.Keys(held)) {
		rows = append(rows, syncHeldVM{ID: id, Reason: held[id]})
	}
	return rows
}

// loadPartitionSync reads recorded hosted-push state. Missing file is empty
// (no marker, no false green). Never contacts satelle.dev.
func loadPartitionSync(repoPath string) syncstate.State {
	if strings.TrimSpace(repoPath) == "" {
		return syncstate.State{}
	}
	home := homeDir()
	if home == "" {
		return syncstate.State{}
	}
	st, _, err := syncstate.Load(home, repoPath)
	if err != nil {
		return syncstate.State{}
	}
	return st
}

func displayedServerLogPath() string {
	home := homeDir()
	if home == "" {
		return filepath.Join(mirror.DefaultDirName, "server.log")
	}
	return mirror.ServerLogPath(home)
}

// homeDir is the satelle home satelled reads machine state from:
// SATELLE_HOME, else empty under go test (a unit test never reads the real
// home), else config.GlobalDir().
func homeDir() string {
	if home := strings.TrimSpace(os.Getenv("SATELLE_HOME")); home != "" {
		return home
	}
	if testing.Testing() {
		return ""
	}
	return config.GlobalDir()
}

// seatStanding classes a pushed seat from this machine (seatlife.StandingOf):
// here, elsewhere, gone or unverified, with the evidence for it. satelled
// reads the machine id and the session records and never writes either.
type seatStanding func(mirror.Seat) (seatlife.Standing, seatlife.Evidence)

// standingHere is the seatStanding of this machine's home.
func standingHere() seatStanding {
	home := homeDir()
	here, _ := config.ReadMachineID(home)
	return func(s mirror.Seat) (seatlife.Standing, seatlife.Evidence) {
		return seatlife.StandingOf(home, here, seatOf(s))
	}
}

// seatOf is a pushed seat row as the item.Seat it was folded from.
func seatOf(s mirror.Seat) item.Seat {
	return item.Seat{Item: s.Item, Session: s.Session, Location: s.Location, Base: s.Base, BoundAt: s.BoundAt, Machine: s.Machine, Host: s.Host, User: s.User}
}

// partitionFreshness reports when this partition was last confirmed against its
// repo, by a push or the automatic sync pass's confirm (sty_e6e467fe,
// sto_128803b3), and whether that is longer ago than mirror.StaleAfter. An
// unknown partition returns the zero time and stale: the page then says it has
// no confirmation at all, which is the honest reading.
func partitionFreshness(ctx context.Context, s *mirror.Store, repoKey string) (last time.Time, stale bool) {
	p, ok, err := s.GetPartition(ctx, repoKey)
	if err != nil || !ok {
		return time.Time{}, true
	}
	last, _ = p.LastIngest()
	return last.Local(), p.Stale(time.Now())
}

// mirrorLoadPanels assembles pageData exclusively from mirror kinds (no repo DB).
func mirrorLoadPanels(ctx context.Context, s *mirror.Store, repoKey, slug string) (pageData, mirror.IdentityMeta, error) {
	id := mirrorIdentity(ctx, s, repoKey)

	stories, err := decodeItems(ctx, s, repoKey, "story")
	if err != nil {
		return pageData{}, id, err
	}
	tasks, err := decodeItems(ctx, s, repoKey, "task")
	if err != nil {
		return pageData{}, id, err
	}
	executions, err := decodeItems(ctx, s, repoKey, "execution")
	if err != nil {
		return pageData{}, id, err
	}
	queues, err := decodeItems(ctx, s, repoKey, "queue")
	if err != nil {
		return pageData{}, id, err
	}
	docs, err := decodeDocs(ctx, s, repoKey)
	if err != nil {
		return pageData{}, id, err
	}
	workflows := workflowsOf(docs)
	seats, err := decodeSeats(ctx, s, repoKey)
	if err != nil {
		return pageData{}, id, err
	}
	standing := standingHere()
	listed := offPanels(stories, workflows)
	// One rule marks every story row, on the Stories tab or a panel, in every
	// mode, and the Stories tab's counts read the same marks.
	eng := engagements(seats, stories, workflows, standing)
	engagedIDs, elsewhereIDs := engagedCounts(eng, listed)
	events, err := decodeLedger(ctx, s, repoKey, "")
	if err != nil {
		return pageData{}, id, err
	}

	projectName := id.ProjectName
	if projectName == "" {
		projectName = slug
	}
	repoRoot := id.RepoRoot
	if repoRoot == "" {
		repoRoot = slug
	}

	syncSt := loadPartitionSync(repoRoot)
	bySeat := seatsByItem(seats)
	storyRows := itemRows(stories, workflows)
	for i := range storyRows {
		storyRows[i].Engaged = eng[storyRows[i].ID]
	}
	withAssignees(storyRows, bySeat, standing)
	storyIDs := map[string]bool{}
	for _, it := range stories {
		storyIDs[it.ID] = true
	}
	withProgress(storyRows, eventsByItem(events, storyIDs), bySeat, workflows, standing)
	storyRows, panels := splitPanels(storyRows, workflows)
	taskRows := itemRows(tasks, workflows)
	withAssignees(taskRows, bySeat, standing)
	queueVMs := queueRows(queues, slices.Concat(stories, tasks, executions, queues), events, workflows)
	for i := range queueVMs {
		queueVMs[i].Assignees = assigneeVMs(assigneesOf(queueVMs[i].Item, bySeat[queueVMs[i].ID], standing))
	}
	lastIngest, stale := partitionFreshness(ctx, s, repoKey)
	return pageData{
		RepoRoot:          repoRoot,
		ProjectName:       projectName,
		LastIngest:        lastIngest,
		Stale:             stale,
		SyncLastSuccess:   syncSt.SyncLastSuccess,
		SyncReason:        syncSt.SyncReason,
		SyncLocal:         syncSt.SyncStore == syncstate.LocalStore,
		SyncStore:         syncSt.SyncStore,
		SyncUpgrade:       syncSt.SyncUpgrade,
		SyncHeld:          syncHeldRows(syncSt.PushHeld),
		Stories:           storyRows,
		Panels:            panels,
		BacklogCount:      backlogCount(listed, workflows),
		EngagementCount:   len(engagedIDs),
		EngagedStoryIDs:   engagedIDs,
		ElsewhereCount:    len(elsewhereIDs),
		ElsewhereStoryIDs: elsewhereIDs,
		Tasks:             taskRows,
		Queues:            queueVMs,
		Activity:          activityRows(stories, executions, workflows, seats, events, standing),
		DocKinds:          docKinds(docs),
		Workflows:         workflowRows(docs),
		Uptime:            formatUptime(time.Since(serverStart)),
		TopBar:            newTopBar(ctx, s, ""),
		Hosted:            hostedLinks(id.Hosted),
	}, id, nil
}

// hostedLinks is a hosted-mode partition's link to the server's project
// page; a local partition links nothing. Every story row in the local store
// is listed whatever engaged it (sty_6ce071dc).
func hostedLinks(h *mirror.HostedMeta) *hostedLinksVM {
	if h == nil || h.Server == "" || h.Project == "" {
		return nil
	}
	return &hostedLinksVM{Project: strings.TrimRight(h.Server, "/") + "/app/p/" + url.PathEscape(h.Project)}
}

// seatsByItem groups seats by the item each is bound to.
func seatsByItem(seats []mirror.Seat) map[string][]mirror.Seat {
	by := map[string][]mirror.Seat{}
	for _, s := range seats {
		if id := seatItem(s); id != "" {
			by[id] = append(by[id], s)
		}
	}
	return by
}

// assigneesOf is it's assignees and those of them working it
// (item.AssigneesOf), from seats (the item's) kept where their standing is
// working: a seat proven gone drops its user.
func assigneesOf(it Item, seats []mirror.Seat, standing seatStanding) (users, working []string) {
	held := slices.Clone(seats)
	sort.SliceStable(held, func(i, j int) bool { return held[i].BoundAt.Before(held[j].BoundAt) })
	var live []item.Seat
	for _, s := range held {
		if st, _ := standing(s); st.Working() {
			live = append(live, item.Seat{Session: s.Session, Location: s.Location, Machine: s.Machine, Host: s.Host, User: s.User})
		}
	}
	return item.AssigneesOf(it.Assignees, live)
}

// assigneeVMs is users as view rows, each in working marked.
func assigneeVMs(users, working []string) []assigneeVM {
	out := make([]assigneeVM, len(users))
	for i, u := range users {
		out[i] = assigneeVM{User: u, Working: slices.Contains(working, u)}
	}
	return out
}

// withAssignees sets each row's assignees from bySeat (seatsByItem).
func withAssignees(rows []rowVM, bySeat map[string][]mirror.Seat, standing seatStanding) {
	for i := range rows {
		rows[i].Assignees = assigneeVMs(assigneesOf(rows[i].Item, bySeat[rows[i].ID], standing))
	}
}

// withProgress sets each row's step chips (progressOf) from its events
// (eventsByItem, oldest first), its seats (seatsByItem) and its workflow.
func withProgress(rows []rowVM, byItem map[string][]Entry, bySeat map[string][]mirror.Seat, workflows map[string]*mirror.Workflow, standing seatStanding) {
	for i := range rows {
		it := rows[i].Item
		rows[i].Progress = progressOf(workflows[it.Workflow], it, byItem[it.ID], bySeat[it.ID], standing)
	}
}

// itemRows is one row per item, its status classed by its workflow.
func itemRows(items []Item, workflows map[string]*mirror.Workflow) []rowVM {
	out := make([]rowVM, len(items))
	for i, it := range items {
		out[i] = rowVM{Item: it, StateClass: stateClass(workflows[it.Workflow], it.Status)}
	}
	return out
}

func decodeItems(ctx context.Context, s *mirror.Store, repoKey, kind string) ([]Item, error) {
	rows, err := s.ListItems(ctx, repoKey, kind)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		var it Item
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

// mirrorDoc is a pushed doc row.
type mirrorDoc = docEntry

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

// workflowsOf indexes the pushed workflows by name.
func workflowsOf(docs []mirrorDoc) map[string]*mirror.Workflow {
	out := map[string]*mirror.Workflow{}
	for _, d := range docs {
		if d.Kind == mirror.DocKindWorkflows && d.Workflow != nil {
			out[d.Name] = d.Workflow
		}
	}
	return out
}

// workflowRows is the Workflow panel.
func workflowRows(docs []mirrorDoc) []workflowRowVM {
	var out []workflowRowVM
	for _, d := range docs {
		if d.Kind != mirror.DocKindWorkflows {
			continue
		}
		vm := workflowRowVM{Name: d.Name, Source: d.Source, Updated: d.ModTime}
		if d.Workflow != nil {
			vm.States = len(d.Workflow.States)
		}
		out = append(out, vm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// docKinds groups the pushed docs by kind for the docs fragment.
func docKinds(docs []mirrorDoc) []kindGroup {
	byKind := map[string][]docRowVM{}
	for _, d := range docs {
		byKind[d.Kind] = append(byKind[d.Kind], docRowVM{Name: d.Name, Headline: d.Headline, ModTime: d.ModTime, Source: d.Source})
	}
	var out []kindGroup
	for _, k := range slices.Sorted(maps.Keys(byKind)) {
		rows := byKind[k]
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		out = append(out, kindGroup{Kind: k, Docs: rows})
	}
	return out
}

// decodeLedger returns the item's events oldest first ("" = every item's).
func decodeLedger(ctx context.Context, s *mirror.Store, repoKey, itemID string) ([]Entry, error) {
	rows, err := s.ListItems(ctx, repoKey, "ledger_event")
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, r := range rows {
		var e Entry
		if err := json.Unmarshal([]byte(r.Payload), &e); err != nil {
			continue
		}
		e.Normalize()
		if e.ID == "" {
			e.ID = r.ID
		}
		if itemID != "" && e.Item != itemID {
			continue
		}
		out = append(out, e)
	}
	sortEntries(out)
	return out, nil
}

// decodeSeats returns the partition's seat rows.
func decodeSeats(ctx context.Context, s *mirror.Store, repoKey string) ([]mirror.Seat, error) {
	rows, err := s.ListItems(ctx, repoKey, "seat")
	if err != nil {
		return nil, err
	}
	seats := make([]mirror.Seat, 0, len(rows))
	for _, r := range rows {
		var seat mirror.Seat
		if err := json.Unmarshal([]byte(r.Payload), &seat); err != nil {
			continue
		}
		if seat.ID == "" {
			seat.ID = r.ID
		}
		seats = append(seats, seat)
	}
	return seats, nil
}

// seatItem is the item a seat is bound to, "" when none: a legacy row counts
// when it is a story seat that is not stale.
func seatItem(seat mirror.Seat) string {
	id := strings.TrimSpace(seat.Item)
	if id == "" && seat.StorySeat && !seat.Stale {
		id = strings.TrimSpace(seat.ID) // a legacy row: id is the item
	}
	return id
}

// engagements is where each story is engaged, by id; a story engaged
// nowhere is absent. It is the one rule satelled maps a seat's standing to a
// mark by. A seat counts when it is bound to one of stories in a state its
// pushed workflow does not class terminal (a story whose workflow is not
// pushed, or does not name its state, counts, since the page cannot prove it
// terminal). Then by its standing:
//   - here or unverified: the story is engaged here — a seat counts as here
//     until it is proven elsewhere or gone — and the mark is verified when any
//     such seat is here;
//   - elsewhere: one mark per place (elsewhereLabel), seats sharing a label
//     sharing the mark;
//   - gone: nothing.
//
// A task's or task run's seat is not a story's: Activity shows it.
func engagements(seats []mirror.Seat, stories []Item, workflows map[string]*mirror.Workflow, standing seatStanding) map[string]*engagementVM {
	byID := make(map[string]Item, len(stories))
	for _, it := range stories {
		byID[it.ID] = it
	}
	held := slices.Clone(seats)
	sort.SliceStable(held, func(i, j int) bool {
		if !held[i].BoundAt.Equal(held[j].BoundAt) {
			return held[i].BoundAt.Before(held[j].BoundAt)
		}
		return held[i].ID < held[j].ID
	})
	type marks struct {
		here      []string
		verified  bool
		elsewhere map[string][]string
	}
	by := map[string]*marks{}
	for _, seat := range held {
		id := seatItem(seat)
		it, ok := byID[id]
		if id == "" || !ok || stateClass(workflows[it.Workflow], it.Status) == "terminal" {
			continue
		}
		st, ev := standing(seat)
		if st == seatlife.Gone {
			continue
		}
		m := by[id]
		if m == nil {
			m = &marks{elsewhere: map[string][]string{}}
			by[id] = m
		}
		switch st {
		case seatlife.Here:
			m.verified = true
			m.here = append(m.here, fmt.Sprintf("engaged here %s, session %s (%s)", seatWho(seat), seatSession(seat), ev))
		case seatlife.Unverified:
			m.here = append(m.here, fmt.Sprintf("engaged here, unverified: %s, %s, session %s", ev, seatWho(seat), seatSession(seat)))
		default:
			label, title := elsewhereMark(seat)
			m.elsewhere[label] = append(m.elsewhere[label], title)
		}
	}
	out := make(map[string]*engagementVM, len(by))
	for id, m := range by {
		e := &engagementVM{}
		if len(m.here) > 0 {
			e.Here = &markVM{Label: "here", Title: strings.Join(m.here, "; "), Unverified: !m.verified}
		}
		for _, label := range slices.Sorted(maps.Keys(m.elsewhere)) {
			e.Elsewhere = append(e.Elsewhere, markVM{Label: label, Title: strings.Join(m.elsewhere[label], "; ")})
		}
		out[id] = e
	}
	return out
}

// engagedCounts is the sorted ids of stories engaged here and of those with a
// seat elsewhere, from eng (engagements): the Stories tab's two badges.
func engagedCounts(eng map[string]*engagementVM, stories []Item) (here, elsewhere []string) {
	for _, it := range stories {
		e := eng[it.ID]
		if e == nil {
			continue
		}
		if e.Here != nil {
			here = append(here, it.ID)
		}
		if len(e.Elsewhere) > 0 {
			elsewhere = append(elsewhere, it.ID)
		}
	}
	slices.Sort(here)
	slices.Sort(elsewhere)
	return here, elsewhere
}

// elsewhereMark is a seat elsewhere's mark label and hover entry: on another
// machine, "<user's name>@<host>" (the place alone when no user is recorded)
// and "engaged by <user> on <host> (<machine id>)"; at a remote or cloud
// location, the place (seatlife.Where) and "engaged by <user> at <location>".
func elsewhereMark(seat mirror.Seat) (label, title string) {
	place, machine := seatlife.Where(seatOf(seat))
	if machine {
		label = place
		if name, _, _ := strings.Cut(seat.User, "@"); name != "" {
			label = name + "@" + place
		}
		host := seat.Host
		if host == "" {
			host = "an unnamed host"
		}
		return label, fmt.Sprintf("engaged %s on %s (%s), session %s", seatWho(seat), host, seat.Machine, seatSession(seat))
	}
	loc := strings.TrimSpace(seat.Location)
	if loc == "" {
		loc = "location not recorded"
	}
	return place, fmt.Sprintf("engaged %s at %s, session %s", seatWho(seat), loc, seatSession(seat))
}

// seatWho is "by <user>" for a seat's hover, or "by an unrecorded user".
func seatWho(seat mirror.Seat) string {
	if u := strings.TrimSpace(seat.User); u != "" {
		return "by " + u
	}
	return "by an unrecorded user"
}

// seatSession is a seat's session for its hover, or "not recorded".
func seatSession(seat mirror.Seat) string {
	if s := strings.TrimSpace(seat.Session); s != "" {
		return s
	}
	return "not recorded"
}

// panelOf is the panel the item's workflow declares, or "".
func panelOf(workflows map[string]*mirror.Workflow, it Item) string {
	if w := workflows[it.Workflow]; w != nil {
		return w.Panel
	}
	return ""
}

// offPanels is the stories listed on the Stories tab: those whose workflow
// declares no panel. The tab's count, its backlog badge and the engaged count
// read these alone.
func offPanels(stories []Item, workflows map[string]*mirror.Workflow) []Item {
	var out []Item
	for _, it := range stories {
		if panelOf(workflows, it) == "" {
			out = append(out, it)
		}
	}
	return out
}

// panelTopic is a panel's tab and fragment key: "wf-" and the name's
// lowercase slug, so it never collides with a built-in tab.
func panelTopic(name string) string {
	var b strings.Builder
	b.WriteString("wf-")
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > len("wf-") {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	return b.String()
}

// splitPanels moves each story row whose workflow declares a panel off the
// Stories rows onto that panel. Every pushed workflow that declares a panel
// yields one, even with no rows; workflows sharing a panel name share it.
// Panels are sorted by name; Open counts rows not in a terminal state.
func splitPanels(rows []rowVM, workflows map[string]*mirror.Workflow) ([]rowVM, []panelVM) {
	byName := map[string]*panelVM{}
	for _, w := range workflows {
		if w != nil && w.Panel != "" && byName[w.Panel] == nil {
			byName[w.Panel] = &panelVM{Name: w.Panel, Topic: panelTopic(w.Panel), Rows: []rowVM{}}
		}
	}
	stories := make([]rowVM, 0, len(rows))
	for _, r := range rows {
		p := byName[panelOf(workflows, r.Item)]
		if p == nil {
			stories = append(stories, r)
			continue
		}
		p.Rows = append(p.Rows, r)
		if r.StateClass != "terminal" {
			p.Open++
		}
	}
	panels := make([]panelVM, 0, len(byName))
	for _, n := range slices.Sorted(maps.Keys(byName)) {
		panels = append(panels, *byName[n])
	}
	return stories, panels
}

// backlogCount is the number of stories in backlog, the Stories tab's
// 'N backlog' badge: those whose state is their workflow's start state, read
// from the pushed workflow, never a state name. A story whose workflow is
// unpushed or lacks its state is not counted. The server owns the count: first
// load renders it and the live refresh refetches it as a fragment.
func backlogCount(stories []Item, workflows map[string]*mirror.Workflow) int {
	n := 0
	for _, it := range stories {
		if s, ok := workflowState(workflows[it.Workflow], it.Status); ok && s.Start {
			n++
		}
	}
	return n
}

// mirrorLoadDetail builds the detail VM from mirror kinds only: the item, its
// route, its verdicts, its attachments, its children and its timeline.
func mirrorLoadDetail(ctx context.Context, s *mirror.Store, repoKey, group, id string) (detailData, mirror.IdentityMeta, error) {
	payload, err := s.GetItem(ctx, repoKey, group, id)
	if err != nil {
		return detailData{}, mirror.IdentityMeta{}, err
	}
	var it Item
	if err := json.Unmarshal([]byte(payload), &it); err != nil {
		return detailData{}, mirror.IdentityMeta{}, err
	}
	if it.ID == "" {
		it.ID = id
	}
	docs, _ := decodeDocs(ctx, s, repoKey)
	workflows := workflowsOf(docs)
	events, _ := decodeLedger(ctx, s, repoKey, id)

	var atts []attachmentVM
	present := map[string]bool{}
	if rows, err := s.ListItems(ctx, repoKey, "story_doc"); err == nil {
		for _, r := range rows {
			var a mirror.Attachment
			if json.Unmarshal([]byte(r.Payload), &a) != nil {
				continue
			}
			if a.Item == "" {
				owner, name, _ := strings.Cut(r.ID, "/")
				a.Item = owner
				if a.Name == "" {
					a.Name = name
				}
			}
			if a.Item != id {
				continue
			}
			atts = append(atts, attachmentView(a))
			present[a.Name] = true
		}
	}
	sort.SliceStable(atts, func(i, j int) bool { return atts[i].At.Before(atts[j].At) })

	var children []childLink
	for _, kind := range []string{"story", "task", "execution", "queue"} {
		items, _ := decodeItems(ctx, s, repoKey, kind)
		for _, c := range items {
			if c.ParentID == id {
				children = append(children, childLink{ID: c.ID, Kind: c.Kind, Title: c.Title, Status: c.Status})
			}
		}
	}
	parentKind := ""
	if it.ParentID != "" {
		for _, kind := range []string{"story", "task", "execution", "queue"} {
			if _, err := s.GetItem(ctx, repoKey, kind, it.ParentID); err == nil {
				parentKind = kind
				break
			}
		}
	}

	// The timeline is newest first.
	evs := make([]eventVM, len(events))
	for i, e := range events {
		ev := eventVM{Entry: e, Chips: eventChips(e)}
		if e.Kind == mirror.EventAttachmentAdded {
			var a struct{ Name string }
			_ = json.Unmarshal(e.Payload, &a)
			ev.DocName = a.Name
			if present[a.Name] {
				ev.DocHref = group + "/" + it.ID + "#" + docAnchor(a.Name)
			}
		}
		evs[len(events)-1-i] = ev
	}

	idMeta := mirrorIdentity(ctx, s, repoKey)
	route := buildRoute(workflows[it.Workflow], it.Workflow, it.Status, events)
	backTab := ""
	if p := panelOf(workflows, it); p != "" {
		backTab = panelTopic(p)
	}
	seats, _ := decodeSeats(ctx, s, repoKey)
	standing := standingHere()
	itemSeats := seatsByItem(seats)[it.ID]
	assignees := assigneeVMs(assigneesOf(it, itemSeats, standing))
	var progress []stageVM
	if group == "story" {
		progress = progressOf(workflows[it.Workflow], it, events, itemSeats, standing)
	}
	return detailData{
		Item:          it,
		Progress:      progress,
		Assignees:     assignees,
		BackTab:       backTab,
		StateClass:    stateClass(workflows[it.Workflow], it.Status),
		ParentKind:    parentKind,
		Children:      children,
		Route:         route,
		Presentations: buildPresentations(events),
		Attachments:   atts,
		Events:        evs,
		TopBar:        newTopBar(ctx, s, ""),
	}, idMeta, nil
}

// docAnchor returns the fragment id for an attached document (doc-<slug>).
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
	return strings.TrimRight(b.String(), "-")
}

// mirrorSettingsData loads the pushed settings blob into the settings template VM.
func mirrorSettingsData(ctx context.Context, s *mirror.Store, repoKey string) (settingsData, mirror.IdentityMeta, error) {
	id := mirrorIdentity(ctx, s, repoKey)
	payload, err := s.GetItem(ctx, repoKey, "settings", "config")
	if err != nil || payload == "" {
		return settingsData{
			TopBar:   newTopBar(ctx, s, ""),
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
		TopBar:   newTopBar(ctx, s, ""),
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
