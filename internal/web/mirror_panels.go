package web

import (
	"context"
	"encoding/json"
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
// here, elsewhere, gone or unverified. satelled reads the machine id and the
// session records and never writes either.
type seatStanding func(mirror.Seat) seatlife.Standing

// standingHere is the seatStanding of this machine's home.
func standingHere() seatStanding {
	home := homeDir()
	here, _ := config.ReadMachineID(home)
	return func(s mirror.Seat) seatlife.Standing {
		st, _ := seatlife.StandingOf(home, here, item.Seat{Session: s.Session, Location: s.Location, Machine: s.Machine, Host: s.Host})
		return st
	}
}

// partitionFreshness reports when this partition was last confirmed against its
// repo (sty_e6e467fe) and whether that is longer ago than mirror.StaleAfter. An
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
	engagedIDs := engagedItems(seats, stories, workflows, standing)
	here, links := hostedView(id.Hosted, engagedIDs)
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
	storyRows := itemRows(stories, workflows)
	for i := range storyRows {
		storyRows[i].Here = here[storyRows[i].ID]
	}
	lastIngest, stale := partitionFreshness(ctx, s, repoKey)
	return pageData{
		RepoRoot:        repoRoot,
		ProjectName:     projectName,
		LastIngest:      lastIngest,
		Stale:           stale,
		SyncLastSuccess: syncSt.SyncLastSuccess,
		SyncReason:      syncSt.SyncReason,
		SyncLocal:       syncSt.SyncStore == syncstate.LocalStore,
		SyncStore:       syncSt.SyncStore,
		SyncHeld:        syncHeldRows(syncSt.PushHeld),
		Stories:         storyRows,
		BacklogCount:    backlogCount(stories, workflows),
		EngagementCount: len(engagedIDs),
		EngagedStoryIDs: engagedIDs,
		Tasks:           itemRows(tasks, workflows),
		Queues:          queueRows(queues, slices.Concat(stories, tasks, executions, queues), events, workflows),
		Activity:        activityRows(stories, executions, workflows, seats, events, standing),
		DocKinds:        docKinds(docs),
		Workflows:       workflowRows(docs),
		Uptime:          formatUptime(time.Since(serverStart)),
		TopBar:          newTopBar(ctx, s, ""),
		Hosted:          links,
	}, id, nil
}

// hostedView is a hosted-mode partition's here-set — the stories engaged on
// this machine (engaged, from seats classed here), which the list marks but
// never filters on — and the link to the server's project page. Every story
// row in the local store is listed whatever engaged it (sty_6ce071dc). A local
// partition marks nothing and links nothing.
func hostedView(h *mirror.HostedMeta, engaged []string) (map[string]bool, *hostedLinksVM) {
	if h == nil || h.Server == "" || h.Project == "" {
		return nil, nil
	}
	here := make(map[string]bool, len(engaged))
	for _, id := range engaged {
		here[id] = true
	}
	return here, &hostedLinksVM{Project: strings.TrimRight(h.Server, "/") + "/app/p/" + url.PathEscape(h.Project)}
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

// engagedItems is the sorted set of stories engaged here now: the items the
// seats classed here (bound on this machine, session alive) are bound to that
// are stories in a non-terminal state. A seat elsewhere, gone or unverified is
// not counted. A task run's (execution's) seat is not a story engaged —
// Activity lists it instead — and a story whose workflow does not name its
// state counts, since the page cannot prove it terminal.
func engagedItems(seats []mirror.Seat, stories []Item, workflows map[string]*mirror.Workflow, standing seatStanding) []string {
	byID := make(map[string]Item, len(stories))
	for _, it := range stories {
		byID[it.ID] = it
	}
	set := map[string]bool{}
	for _, seat := range seats {
		id := seatItem(seat)
		it, ok := byID[id]
		if id == "" || !ok || stateClass(workflows[it.Workflow], it.Status) == "terminal" || standing(seat) != seatlife.Here {
			continue
		}
		set[id] = true
	}
	return slices.Sorted(maps.Keys(set))
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
	return detailData{
		Item:          it,
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
