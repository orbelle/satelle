package ledgerrepl

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/seatlife"
	"github.com/bobmcallan/satelle/internal/wire"
	"github.com/bobmcallan/satelle/internal/wire/chainpb"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

var HeadsPageSize int32 = 200

const attachmentsPageSize int32 = 200

const PerformerOutputPrefix = "performer-output-"

type Dev struct {
	Client  *hosted.Client
	Project string
	Server  string
	Ledger  *ledger.Store
	Cache   *cache.Cache
	Home    string

	chains map[string][]ledger.Event
	atts   map[string]bool
}

func (d *Dev) Describe() string {
	return fmt.Sprintf("satelle.dev project %s at %s", d.Project, d.Server)
}

func attKey(item, name, sha string) string { return item + "/" + name + "/" + sha }

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
			if c.Len == 0 && c.Head != "" {
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

func (d *Dev) chain(ctx context.Context, item string) ([]ledger.Event, error) {
	if events, ok := d.chains[item]; ok {
		return events, nil
	}
	return d.read(ctx, item)
}

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

func blobName(ref ledger.BlobRef) (name, media string) {
	if ref.Name != "" {
		return ref.Name, ref.MediaType
	}
	return PerformerOutputPrefix + ref.Event, "text/plain"
}

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

func Summary(it item.Item, head string, users, working []string) (*satellepb.Item, error) {
	record, err := json.Marshal(it)
	if err != nil {
		return nil, err
	}
	return &satellepb.Item{
		Id: it.ID, Kind: it.Kind, Title: it.Title, Status: it.Status, Workflow: it.Workflow,
		Parent: it.Parent, Category: it.Category, Tags: it.Tags,
		Assignees: users, Working: working, HeadHash: head, UpdatedAt: timestamppb.New(it.UpdatedAt), Record: record,
	}, nil
}

func (d *Dev) PutSummaries(ctx context.Context, ids []string) error {
	if len(ids) == 0 || d.Cache == nil || d.Ledger == nil {
		return nil
	}
	req := &satellepb.PutItemsRequest{Project: d.Project}
	here, _ := config.ReadMachineID(d.Home)
	for _, id := range ids {
		it, ok, err := d.Cache.Item(id)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		h, err := d.Ledger.Head(id)
		if err != nil {
			return err
		}
		seats, err := d.Cache.Seats("", id)
		if err != nil {
			return err
		}
		users, working := item.AssigneesOf(it.Assignees, seatlife.Working(d.Home, here, seats))
		sum, err := Summary(it, h, users, working)
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
