package ledgerrepl

import (
	"context"
	"encoding/json"
	"fmt"

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

// attachmentsPageSize is the page size of a ListAttachmentChanges walk.
const attachmentsPageSize int32 = 200

// PerformerOutputPrefix names a performer output stored on satelle.dev as an
// attachment: performer-output-<event id>.
const PerformerOutputPrefix = "performer-output-"

// Dev is satelle.dev as a Store, over the existing Chain and Attachment
// services: Append is the conditional append (compare-and-append on the
// head), Heads lists every chain with its head and length, and each blob is
// an attachment of its item. Ledger and Cache give PutSummaries the item
// summaries the server lists.
type Dev struct {
	Client  *hosted.Client
	Project string
	// Server names the server, for Describe.
	Server string
	Ledger *ledger.Store
	Cache  *cache.Cache

	chains map[string][]ledger.Event // each item's chain, read once per run
	atts   map[string]bool           // item/name/sha256 of every stored attachment
}

// Describe names the project and server.
func (d *Dev) Describe() string {
	return fmt.Sprintf("satelle.dev project %s at %s", d.Project, d.Server)
}

func attKey(item, name, sha string) string { return item + "/" + name + "/" + sha }

// Chains walks Chain.Heads (every chain) or reads each named chain.
func (d *Dev) Chains(ctx context.Context, items []string) (Listing, error) {
	l := Listing{Chains: map[string]Chain{}}
	if items != nil {
		for _, item := range items {
			events, err := d.read(ctx, item)
			if err != nil {
				return l, err
			}
			if len(events) > 0 {
				l.Chains[item] = Chain{Len: len(events), Head: events[len(events)-1].Hash, Hole: -1}
			}
		}
		return l, nil
	}
	token := ""
	for {
		resp, err := d.Client.Chain.Heads(ctx, &chainpb.HeadsRequest{Project: d.Project, PageSize: HeadsPageSize, PageToken: token})
		if err != nil {
			return l, hosted.Describe("Chain.Heads", err)
		}
		for _, h := range resp.GetHeads() {
			id := h.GetItemId()
			if !ledger.ValidItem(id) {
				l.Skipped = append(l.Skipped, fmt.Sprintf("the server's item id %s is not a valid item id; skipped", id))
				continue
			}
			c := Chain{Len: int(h.GetSeq()), Head: h.GetHeadHash(), Hole: -1}
			if c.Len == 0 && c.Head != "" { // a server that does not fill seq
				events, err := d.read(ctx, id)
				if err != nil {
					return l, err
				}
				c.Len = len(events)
			}
			if c.Len > 0 {
				l.Chains[id] = c
			}
		}
		if token = resp.GetNextPageToken(); token == "" {
			return l, nil
		}
	}
}

// read reads item's whole chain from the server and keeps it for the run; an
// item the server does not hold is an empty chain.
func (d *Dev) read(ctx context.Context, item string) ([]ledger.Event, error) {
	resp, err := d.Client.Chain.Read(ctx, &chainpb.ReadRequest{Project: d.Project, ItemId: item})
	var events []ledger.Event
	switch {
	case err == nil:
		events = wire.FromProtos(resp.GetEvents())
	case hosted.IsNotFound(err):
	default:
		return nil, hosted.Describe("Chain.Read "+item, err)
	}
	if d.chains == nil {
		d.chains = map[string][]ledger.Event{}
	}
	d.chains[item] = events
	return events, nil
}

// chain is item's chain as read this run, reading it the first time.
func (d *Dev) chain(ctx context.Context, item string) ([]ledger.Event, error) {
	if events, ok := d.chains[item]; ok {
		return events, nil
	}
	return d.read(ctx, item)
}

// HashAt is the hash at seq of the chain read this run.
func (d *Dev) HashAt(ctx context.Context, item string, seq int) (string, error) {
	events, err := d.chain(ctx, item)
	if err != nil {
		return "", err
	}
	if seq < 0 || seq >= len(events) {
		return "", &refusal{fmt.Sprintf("the server's chain %s has %d event(s), not one at %d; %s left untouched", item, len(events), seq, item)}
	}
	return events[seq].Hash, nil
}

// Events is from..to-1 of the chain read this run.
func (d *Dev) Events(ctx context.Context, item string, from, to int) ([]ledger.Event, error) {
	events, err := d.chain(ctx, item)
	if err != nil {
		return nil, err
	}
	if from < 0 || to > len(events) || from > to {
		return nil, &refusal{fmt.Sprintf("the server's chain %s has %d event(s), not %d..%d; nothing appended", item, len(events), from, to-1)}
	}
	return append([]ledger.Event(nil), events[from:to]...), nil
}

// Offer appends events on the head events[0].Prev, all or none. A stale head
// means the server holds an event at seq: it re-reads the chain and returns
// that event's hash.
func (d *Dev) Offer(ctx context.Context, item string, seq int, events []ledger.Event) (int, string, error) {
	if len(events) == 0 {
		return 0, "", nil
	}
	_, err := d.Client.Chain.Append(ctx, &chainpb.AppendRequest{Project: d.Project, ItemId: item, PrevHash: events[0].Prev, Events: wire.ToProtos(events)})
	if err == nil {
		if have, ok := d.chains[item]; ok && len(have) == seq {
			d.chains[item] = append(have, events...)
		} else {
			delete(d.chains, item)
		}
		return len(events), "", nil
	}
	if !staleHead(err) {
		return 0, "", hosted.Describe("Chain.Append "+item, err)
	}
	remote, err := d.read(ctx, item)
	if err != nil {
		return 0, "", err
	}
	if len(remote) > seq {
		return 0, remote[seq].Hash, nil
	}
	return 0, "", fmt.Errorf("the server's chain %s has %d event(s) and does not end at event %d", item, len(remote), seq-1)
}

func staleHead(err error) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return false
	}
	for _, d := range st.Details() {
		if _, ok := d.(*chainpb.StaleHead); ok {
			return true
		}
	}
	return false
}

// blobName is the attachment name a blob is stored under: an attachment's
// own name, or performer-output-<event id> for a performer output.
func blobName(ref ledger.BlobRef) (name, media string) {
	if ref.Name != "" {
		return ref.Name, ref.MediaType
	}
	return PerformerOutputPrefix + ref.Event, "text/plain"
}

// HasBlob looks the attachment up in one ListAttachmentChanges walk per run.
func (d *Dev) HasBlob(ctx context.Context, item string, ref ledger.BlobRef) (bool, error) {
	if d.atts == nil {
		atts := map[string]bool{}
		token := ""
		for {
			resp, err := d.Client.Attachments.ListAttachmentChanges(ctx, &satellepb.ListAttachmentChangesRequest{Project: d.Project, SinceToken: token, PageSize: attachmentsPageSize})
			if err != nil {
				return false, hosted.Describe("AttachmentService.ListAttachmentChanges", err)
			}
			for _, a := range resp.GetAttachments() {
				atts[attKey(a.GetItemId(), a.GetName(), a.GetSha256())] = true
			}
			next := resp.GetNextToken()
			if len(resp.GetAttachments()) == 0 || next == "" || next == token {
				break
			}
			token = next
		}
		d.atts = atts
	}
	name, _ := blobName(ref)
	return d.atts[attKey(item, name, ref.SHA256)], nil
}

// PutBlob stores the body as an attachment of item.
func (d *Dev) PutBlob(ctx context.Context, item string, ref ledger.BlobRef, body []byte) (bool, error) {
	name, media := blobName(ref)
	r, err := d.Client.Attachments.PutAttachment(ctx, &satellepb.PutAttachmentRequest{Attachment: &satellepb.Attachment{
		Project: d.Project, ItemId: item, Name: name, Sha256: ref.SHA256, Size: int64(len(body)), MediaType: media, Body: body,
	}})
	if err != nil {
		return false, hosted.Describe("AttachmentService.PutAttachment "+item+"/"+name, err)
	}
	if d.atts != nil {
		d.atts[attKey(item, name, ref.SHA256)] = true
	}
	return r.GetCreated(), nil
}

// GetBlob gets the attachment of item by name and sha256.
func (d *Dev) GetBlob(ctx context.Context, item string, ref ledger.BlobRef) ([]byte, bool, error) {
	name, _ := blobName(ref)
	a, err := d.Client.Attachments.GetAttachment(ctx, &satellepb.GetAttachmentRequest{Project: d.Project, ItemId: item, Name: name, Sha256: ref.SHA256})
	if hosted.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, hosted.Describe("AttachmentService.GetAttachment "+item+"/"+name, err)
	}
	return a.GetBody(), true, nil
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

// PutSummaries stores the summary of each item the cache folds.
func (d *Dev) PutSummaries(ctx context.Context, ids []string) error {
	if len(ids) == 0 || d.Cache == nil || d.Ledger == nil {
		return nil
	}
	req := &satellepb.PutItemsRequest{Project: d.Project}
	for _, id := range ids {
		it, ok, err := d.Cache.Item(id)
		if err != nil {
			return err
		}
		if !ok {
			continue // a chain of events the fold does not make an item of
		}
		h, err := d.Ledger.Head(id)
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
	if _, err := d.Client.Items.PutItems(ctx, req); err != nil {
		return hosted.Describe("ItemService.PutItems", err)
	}
	return nil
}
