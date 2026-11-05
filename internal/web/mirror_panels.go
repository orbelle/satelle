package web

import (
	"context"
	"encoding/json"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
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
	engagedIDs, _ := decodeEngagedStorySeats(ctx, s, repoKey)
	dispatches, _ := decodeDispatchSeats(ctx, s, repoKey)

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
	storyRows := itemRows(stories, dispatches)
	for i := range storyRows {
		storyRows[i].Here = here[storyRows[i].ID]
	}
	lastIngest, stale := partitionFreshness(ctx, s, repoKey)
	return pageData{
		RepoRoot:        repoRoot,
		ProjectName:     projectName,
		LastIngest:      lastIngest,
		Stale:           stale,
		SyncLastSuccess: syncSt.PushLastSuccess,
		SyncReason:      syncSt.PushReason,
		SyncLocal:       syncSt.Scope == "local",
		SyncLogPath:     logPath,
		SyncHeld:        syncHeldRows(syncSt.PushHeld),
		Stories:         storyRows,
		EngagementCount: len(engagedIDs),
		EngagedStoryIDs: engagedIDs,
		Tasks:           itemRows(tasks, dispatches),
		Activity:        activityRows(itemRows(executions, dispatches)),
		DocCount:        len(docs),
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

// activityRows is the Activity panel: executions that are recorded. Which
// states are under way is the workflow's; the kernel pages of B4 read it.
func activityRows(executions []rowVM) []rowVM {
	return append([]rowVM{}, executions...)
}

// itemRows is one row per item with its running dispatch, if any.
func itemRows(items []Item, dispatches map[string]*dispatchVM) []rowVM {
	out := make([]rowVM, len(items))
	for i, it := range items {
		out[i] = rowVM{Item: it, Dispatch: dispatches[it.ID]}
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

// mirrorDoc is a doc payload with optional provenance/source from the snapshot.
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

func decodeLedgerByStory(ctx context.Context, s *mirror.Store, repoKey string) (map[string][]Entry, error) {
	rows, err := s.ListItems(ctx, repoKey, "ledger_event")
	if err != nil {
		return nil, err
	}
	out := map[string][]Entry{}
	for _, r := range rows {
		var e Entry
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
		sortEntries(es)
		out[sid] = es
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
	var item Item
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return detailData{}, mirror.IdentityMeta{}, err
	}
	if item.ID == "" {
		item.ID = id
	}

	// Story docs from mirror story_doc kind (id = story_id/name), built before
	// the timeline so attachment events can resolve hrefs.
	var docs []storyDocVM
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
			vm := storyDocVM{
				Name: name, Type: ref.Type, Anchor: docAnchor(name),
				HTML: renderMarkdown(ref.Body),
			}
			docs = append(docs, vm)
			presentDocs[name] = true
		}
	}

	entriesByStory, _ := decodeLedgerByStory(ctx, s, repoKey)

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

	idMeta := mirrorIdentity(ctx, s, repoKey)
	return detailData{
		Item: item, Events: evs, Docs: docs, Executions: executions,
		TopBar: mirrorTopBar("", idMeta.FooterEmail),
	}, idMeta, nil
}

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
// (`attached <type> document "<name>"` / binary bodies with the filename quoted).
var attachedQuotedNameRe = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)

// attachedDocRef resolves the document name/type for a story_doc_attached
// ledger entry: prefer the structured payload, fall back to parsing the body
// so pre-payload ledger rows still render a named timeline item.
func attachedDocRef(e Entry) (name, typ string) {
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
