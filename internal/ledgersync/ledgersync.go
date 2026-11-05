// Package ledgersync carries a repo's work state to and from satelle-server:
// item chains (Chain), item summaries (ItemService) and attachment bodies
// (AttachmentService). Nothing else travels: configuration is in git and repo
// documents are gone. A push never rewrites history: an item whose remote
// chain moved on and does not build under the local tail is reported as a
// conflict and left alone.
package ledgersync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire"
	"github.com/bobmcallan/satelle/internal/wire/chainpb"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// HeadsPageSize is the page size of a Chain.Heads walk.
var HeadsPageSize int32 = 200

// Syncer is one repo's ledger against one project on one server.
type Syncer struct {
	Client  *hosted.Client
	Project string // the project id
	Ledger  *ledger.Store
	Cache   *cache.Cache
	// StatePath is the file recording what was pushed (heads per item and
	// attachment bodies), in the runtime directory.
	StatePath string
}

// State is what this runtime last knows the server holds.
type State struct {
	// Heads maps an item to the head hash the server is known to hold.
	Heads map[string]string `json:"heads"`
	// Attachments holds "<item>/<sha256>/<name>" of each body the server holds.
	Attachments map[string]bool `json:"attachments"`
}

func attKey(itemID, sha, name string) string { return itemID + "/" + sha + "/" + name }

// LoadState reads the state file; a missing one is empty.
func (s *Syncer) LoadState() (State, error) {
	st := State{Heads: map[string]string{}, Attachments: map[string]bool{}}
	b, err := os.ReadFile(s.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("sync state %s: %w", s.StatePath, err)
	}
	if st.Heads == nil {
		st.Heads = map[string]string{}
	}
	if st.Attachments == nil {
		st.Attachments = map[string]bool{}
	}
	return st, nil
}

func (s *Syncer) saveState(st State) error {
	if err := os.MkdirAll(filepath.Dir(s.StatePath), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.StatePath)
}

// Conflict is an item whose remote chain diverged from the local one.
type Conflict struct {
	Item string
	// Common is how many events both chains share from the start.
	Common int
	// LocalOnly and RemoteOnly are the events after the shared prefix.
	LocalOnly, RemoteOnly []ledger.Event
}

func (c Conflict) Error() string {
	return fmt.Sprintf("%s diverged after event %d: %d local event(s) and %d remote event(s) differ", c.Item, c.Common, len(c.LocalOnly), len(c.RemoteOnly))
}

// PushResult is what a push did.
type PushResult struct {
	Items       []string // items whose events were appended
	Events      int
	Attachments int
	Conflicts   []Conflict
}

// indexOf is the position of hash on events, -1 when absent ("" is -1).
func indexOf(events []ledger.Event, hash string) int {
	for i, e := range events {
		if e.Hash == hash {
			return i
		}
	}
	return -1
}

// common is the length of the shared prefix of two chains.
func common(a, b []ledger.Event) int {
	n := 0
	for n < len(a) && n < len(b) && a[n].Hash == b[n].Hash {
		n++
	}
	return n
}

func staleHead(err error) (*chainpb.StaleHead, bool) {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return nil, false
	}
	for _, d := range st.Details() {
		if sh, ok := d.(*chainpb.StaleHead); ok {
			return sh, true
		}
	}
	return nil, false
}

// readRemote reads item's whole remote chain.
func (s *Syncer) readRemote(ctx context.Context, itemID, after string) ([]ledger.Event, error) {
	resp, err := s.Client.Chain.Read(ctx, &chainpb.ReadRequest{Project: s.Project, ItemId: itemID, AfterHash: after})
	if err != nil {
		return nil, hosted.Describe("Chain.Read "+itemID, err)
	}
	return wire.FromProtos(resp.GetEvents()), nil
}

// Push appends every item's events the server does not hold, then stores the
// summaries of the items it pushed and every attachment body not yet pushed.
func (s *Syncer) Push(ctx context.Context) (PushResult, error) {
	var res PushResult
	st, err := s.LoadState()
	if err != nil {
		return res, err
	}
	ids, err := s.Ledger.Items()
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		n, conflict, err := s.pushItem(ctx, id, st)
		if err != nil {
			_ = s.saveState(st)
			return res, err
		}
		if conflict != nil {
			res.Conflicts = append(res.Conflicts, *conflict)
			continue
		}
		if n > 0 {
			res.Items = append(res.Items, id)
			res.Events += n
		}
	}
	if err := s.saveState(st); err != nil {
		return res, err
	}
	if err := s.putItems(ctx, res.Items); err != nil {
		return res, err
	}
	n, err := s.pushAttachments(ctx, ids, st, res.Conflicts)
	res.Attachments = n
	if serr := s.saveState(st); err == nil {
		err = serr
	}
	return res, err
}

// pushItem appends item's unpushed events. It returns how many it appended,
// or the conflict when the remote chain diverged.
func (s *Syncer) pushItem(ctx context.Context, id string, st State) (int, *Conflict, error) {
	local, err := s.Ledger.Read(id)
	if err != nil {
		return 0, nil, err
	}
	if len(local) == 0 {
		return 0, nil, nil
	}
	known := st.Heads[id]
	at := indexOf(local, known)
	if known != "" && at < 0 {
		known, at = "", -1 // the recorded head is not local: start over and let the server say
	}
	if at == len(local)-1 {
		return 0, nil, nil
	}
	pending := local[at+1:]
	_, err = s.Client.Chain.Append(ctx, &chainpb.AppendRequest{Project: s.Project, ItemId: id, PrevHash: known, Events: wire.ToProtos(pending)})
	if err == nil {
		st.Heads[id] = local[len(local)-1].Hash
		return len(pending), nil, nil
	}
	if _, stale := staleHead(err); !stale {
		return 0, nil, hosted.Describe("Chain.Append "+id, err)
	}
	// The server's head moved: read its chain and see whether the local tail
	// builds on it.
	remote, err := s.readRemote(ctx, id, "")
	if err != nil {
		return 0, nil, err
	}
	n := common(local, remote)
	switch {
	case n == len(remote) && n == len(local):
		st.Heads[id] = local[n-1].Hash
		return 0, nil, nil
	case n == len(remote): // the server holds a prefix of the local chain
		prev := ""
		if n > 0 {
			prev = remote[n-1].Hash
		}
		if _, err := s.Client.Chain.Append(ctx, &chainpb.AppendRequest{Project: s.Project, ItemId: id, PrevHash: prev, Events: wire.ToProtos(local[n:])}); err != nil {
			if _, stale := staleHead(err); stale {
				return 0, &Conflict{Item: id, Common: n, LocalOnly: local[n:]}, nil
			}
			return 0, nil, hosted.Describe("Chain.Append "+id, err)
		}
		st.Heads[id] = local[len(local)-1].Hash
		return len(local) - n, nil, nil
	case n == len(local): // the server is ahead; a pull brings it in
		st.Heads[id] = local[n-1].Hash
		return 0, nil, nil
	}
	return 0, &Conflict{Item: id, Common: n, LocalOnly: local[n:], RemoteOnly: remote[n:]}, nil
}

// Summary is an item's wire summary with its head hash.
func Summary(it item.Item, head string) (*satellepb.Item, error) {
	record, err := json.Marshal(it)
	if err != nil {
		return nil, err
	}
	return &satellepb.Item{
		Id: it.ID, Kind: it.Kind, Title: it.Title, Status: it.Status, Workflow: it.Workflow,
		Parent: it.Parent, Category: it.Category, Tags: it.Tags, Assignee: it.Assignee,
		HeadHash: head, UpdatedAt: timestamppb.New(it.UpdatedAt), Record: record,
	}, nil
}

func (s *Syncer) putItems(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	req := &satellepb.PutItemsRequest{Project: s.Project}
	for _, id := range ids {
		it, ok, err := s.Cache.Item(id)
		if err != nil {
			return err
		}
		if !ok {
			continue // a chain of events the fold does not make an item of
		}
		h, err := s.Ledger.Head(id)
		if err != nil {
			return err
		}
		sum, err := Summary(it, h)
		if err != nil {
			return err
		}
		req.Items = append(req.Items, sum)
	}
	if len(req.Items) == 0 {
		return nil
	}
	if _, err := s.Client.Items.PutItems(ctx, req); err != nil {
		return hosted.Describe("ItemService.PutItems", err)
	}
	return nil
}

// pushAttachments stores each attachment body recorded on a pushed chain that
// the server does not hold yet.
func (s *Syncer) pushAttachments(ctx context.Context, ids []string, st State, conflicts []Conflict) (int, error) {
	skip := map[string]bool{}
	for _, c := range conflicts {
		skip[c.Item] = true
	}
	n := 0
	for _, id := range ids {
		if skip[id] || st.Heads[id] == "" {
			continue
		}
		events, err := s.Ledger.Read(id)
		if err != nil {
			return n, err
		}
		upto := indexOf(events, st.Heads[id])
		for _, a := range item.FoldAttachments(events[:upto+1]) {
			key := attKey(id, a.SHA256, a.Name)
			if st.Attachments[key] {
				continue
			}
			body, err := s.Ledger.GetBlob(a.SHA256)
			if err != nil {
				return n, fmt.Errorf("attachment %s on %s: %w", a.Name, id, err)
			}
			_, err = s.Client.Attachments.PutAttachment(ctx, &satellepb.PutAttachmentRequest{Attachment: &satellepb.Attachment{
				Project: s.Project, ItemId: id, Name: a.Name, Sha256: a.SHA256, Size: a.Size, MediaType: a.MediaType, Body: body,
			}})
			if err != nil {
				return n, hosted.Describe("AttachmentService.PutAttachment "+id+"/"+a.Name, err)
			}
			st.Attachments[key] = true
			n++
		}
	}
	return n, nil
}

// RemoteHeads walks Chain.Heads to the end.
func (s *Syncer) RemoteHeads(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	token := ""
	for {
		resp, err := s.Client.Chain.Heads(ctx, &chainpb.HeadsRequest{Project: s.Project, PageSize: HeadsPageSize, PageToken: token})
		if err != nil {
			return nil, hosted.Describe("Chain.Heads", err)
		}
		for _, h := range resp.GetHeads() {
			out[h.GetItemId()] = h.GetHeadHash()
		}
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

// PullResult is what a pull did.
type PullResult struct {
	Items       []string // items that received events
	Events      int
	Attachments int
	Conflicts   []Conflict
}

// Pull imports every remote event the local ledger lacks, refreshes the
// cache, and fetches the bodies of imported attachments.
func (s *Syncer) Pull(ctx context.Context) (PullResult, error) {
	var res PullResult
	st, err := s.LoadState()
	if err != nil {
		return res, err
	}
	heads, err := s.RemoteHeads(ctx)
	if err != nil {
		return res, err
	}
	ids := make([]string, 0, len(heads))
	for id := range heads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !ledger.ValidItem(id) {
			continue
		}
		if err := s.pullItem(ctx, id, heads[id], st, &res); err != nil {
			_ = s.saveState(st)
			return res, err
		}
	}
	if err := s.saveState(st); err != nil {
		return res, err
	}
	return res, s.Cache.Sync(s.Ledger)
}

// pullItem imports one item's remote events the local ledger lacks. rh is the
// remote head when known ("" reads whatever follows the local tail).
func (s *Syncer) pullItem(ctx context.Context, id, rh string, st State, res *PullResult) error {
	local, err := s.Ledger.Read(id)
	if err != nil {
		return err
	}
	if rh != "" && indexOf(local, rh) >= 0 { // already held (the local chain may be ahead)
		if st.Heads[id] == "" || indexOf(local, st.Heads[id]) < indexOf(local, rh) {
			st.Heads[id] = rh
		}
		return nil
	}
	after := ""
	if len(local) > 0 {
		after = local[len(local)-1].Hash
	}
	resp, err := s.Client.Chain.Read(ctx, &chainpb.ReadRequest{Project: s.Project, ItemId: id, AfterHash: after})
	var remote []ledger.Event
	switch {
	case err == nil:
		remote = wire.FromProtos(resp.GetEvents())
	case after != "" && hosted.IsNotFound(err):
		// The local head is not on the server: the local chain has unpushed
		// events or diverged. Read the whole chain and compare.
		full, err := s.readRemote(ctx, id, "")
		if err != nil {
			return err
		}
		n := common(local, full)
		if n < len(local) {
			res.Conflicts = append(res.Conflicts, Conflict{Item: id, Common: n, LocalOnly: local[n:], RemoteOnly: full[n:]})
			return nil
		}
		remote = full[n:]
	default:
		return hosted.Describe("Chain.Read "+id, err)
	}
	chain := append(append([]ledger.Event(nil), local...), remote...)
	if err := ledger.VerifyChain(id, chain); err != nil {
		return fmt.Errorf("pull %s: the server's chain does not verify: %w", id, err)
	}
	added, err := s.Ledger.Import(chain)
	if err != nil {
		return fmt.Errorf("pull %s: %w", id, err)
	}
	if added > 0 {
		res.Items = append(res.Items, id)
		res.Events += added
	}
	if rh == "" && len(chain) > 0 {
		rh = chain[len(chain)-1].Hash
	}
	st.Heads[id] = rh
	for _, a := range item.FoldAttachments(remote) {
		got, err := s.fetchAttachment(ctx, id, a)
		if err != nil {
			return err
		}
		if got {
			res.Attachments++
		}
		st.Attachments[attKey(id, a.SHA256, a.Name)] = true
	}
	return nil
}

// PullItem imports one item's chain (and its attachment bodies) from the
// server, as a node does for the item it was woken for.
func (s *Syncer) PullItem(ctx context.Context, id string) (PullResult, error) {
	var res PullResult
	st, err := s.LoadState()
	if err != nil {
		return res, err
	}
	if err := s.pullItem(ctx, id, "", st, &res); err != nil {
		_ = s.saveState(st)
		return res, err
	}
	if err := s.saveState(st); err != nil {
		return res, err
	}
	return res, s.Cache.Sync(s.Ledger)
}

// fetchAttachment stores an attachment's body locally unless it is already
// held. It reports whether it fetched one.
func (s *Syncer) fetchAttachment(ctx context.Context, itemID string, a item.Attachment) (bool, error) {
	if _, err := s.Ledger.GetBlob(a.SHA256); err == nil {
		return false, nil
	}
	got, err := s.Client.Attachments.GetAttachment(ctx, &satellepb.GetAttachmentRequest{Project: s.Project, ItemId: itemID, Name: a.Name, Sha256: a.SHA256})
	if err != nil {
		return false, hosted.Describe("AttachmentService.GetAttachment "+itemID+"/"+a.Name, err)
	}
	sha, err := s.Ledger.PutBlob(got.GetBody())
	if err != nil {
		return false, err
	}
	if sha != a.SHA256 {
		return false, fmt.Errorf("attachment %s on %s: the server's body hashes to %s, not %s", a.Name, itemID, sha, a.SHA256)
	}
	return true, nil
}

// ItemStatus compares one item's local and remote heads.
type ItemStatus struct {
	Item   string `json:"item"`
	Local  string `json:"local_head"`
	Remote string `json:"remote_head"`
	State  string `json:"state"`
}

// Item states Status reports.
const (
	InSync     = "in sync"
	Ahead      = "ahead"       // local events to push
	Behind     = "behind"      // remote events to pull
	Diverged   = "diverged"    // both moved; push reports the conflict
	LocalOnly  = "local only"  // never pushed
	RemoteOnly = "remote only" // never pulled
)

// Status compares every item's local head with the server's.
func (s *Syncer) Status(ctx context.Context) ([]ItemStatus, error) {
	st, err := s.LoadState()
	if err != nil {
		return nil, err
	}
	heads, err := s.RemoteHeads(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := s.Ledger.Items()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []ItemStatus
	for _, id := range ids {
		seen[id] = true
		local, err := s.Ledger.Read(id)
		if err != nil {
			return nil, err
		}
		lh := ""
		if len(local) > 0 {
			lh = local[len(local)-1].Hash
		}
		rh := heads[id]
		is := ItemStatus{Item: id, Local: lh, Remote: rh}
		switch at := indexOf(local, rh); {
		case rh == "":
			is.State = LocalOnly
		case rh == lh:
			is.State = InSync
		case at >= 0:
			is.State = Ahead
		case lh == "" || lh == st.Heads[id]:
			is.State = Behind
		default:
			is.State = Diverged
		}
		out = append(out, is)
	}
	for id, rh := range heads {
		if !seen[id] {
			out = append(out, ItemStatus{Item: id, Remote: rh, State: RemoteOnly})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Item < out[j].Item })
	return out, nil
}
