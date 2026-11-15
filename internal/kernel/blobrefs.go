package kernel

import (
	"fmt"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func BlobRefs(events []ledger.Event) []ledger.BlobRef {
	var out []ledger.BlobRef
	add := func(seq int, e ledger.Event, sha, what, name, media string) {
		if ledger.ValidBlobID(sha) {
			out = append(out, ledger.BlobRef{Seq: seq, Event: e.ID, Kind: e.Kind, SHA256: sha, What: what, Name: name, MediaType: media})
		}
	}
	for i, e := range events {
		switch e.Kind {
		case item.KindAttachmentAdd:
			var a item.Attachment
			if e.Decode(&a) == nil {
				add(i, e, a.SHA256, "attachment "+a.Name, a.Name, a.MediaType)
			}
		case KindPerformerRun:
			var r PerformerRun
			if e.Decode(&r) == nil {
				add(i, e, r.Output, fmt.Sprintf("performer output (%s)", r.State), "", "")
			}
		}
	}
	return out
}
