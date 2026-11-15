package wire

import (
	"bytes"
	"encoding/json"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire/chainpb"
)

func ToProto(e ledger.Event) *chainpb.Event {
	return &chainpb.Event{
		Id: e.ID, ItemId: e.Item, Kind: e.Kind, Actor: e.Actor,
		At: timestamppb.New(e.At), Payload: append([]byte(nil), e.Payload...),
		PrevHash: e.Prev, Hash: e.Hash,
	}
}

func FromProto(p *chainpb.Event) ledger.Event {
	e := ledger.Event{
		ID: p.GetId(), Item: p.GetItemId(), Kind: p.GetKind(), Actor: p.GetActor(),
		Prev: p.GetPrevHash(), Hash: p.GetHash(),
	}
	if p.GetAt() != nil {
		e.At = p.GetAt().AsTime().UTC()
	}
	if pl := bytes.TrimSpace(p.GetPayload()); len(pl) > 0 {
		e.Payload = json.RawMessage(append([]byte(nil), p.GetPayload()...))
	}
	return e
}

func FromProtos(ps []*chainpb.Event) []ledger.Event {
	out := make([]ledger.Event, len(ps))
	for i, p := range ps {
		out[i] = FromProto(p)
	}
	return out
}

func ToProtos(es []ledger.Event) []*chainpb.Event {
	out := make([]*chainpb.Event, len(es))
	for i, e := range es {
		out[i] = ToProto(e)
	}
	return out
}

func VerifyHash(p *chainpb.Event) bool {
	h, err := ledger.HashOf(FromProto(p))
	return err == nil && h == p.GetHash()
}
