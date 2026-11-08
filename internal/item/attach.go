package item

import (
	"errors"
	"mime"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// AppendAttachment stores data as a blob and records a, with its sha and size
// filled in, on item id's chain: the one writer of attachment.added. The
// caller checks the item exists and syncs any cache.
func AppendAttachment(l *ledger.Store, id string, a Attachment, data []byte, actor string) (Attachment, ledger.Event, error) {
	if strings.TrimSpace(a.Name) == "" {
		return Attachment{}, ledger.Event{}, errors.New("an attachment needs a name")
	}
	if a.MediaType == "" {
		a.MediaType = "application/octet-stream"
	}
	sha, err := l.PutBlob(data)
	if err != nil {
		return Attachment{}, ledger.Event{}, err
	}
	a.Item, a.At, a.Actor = "", time.Time{}, ""
	a.SHA256, a.Size = sha, int64(len(data))
	e, err := l.Append(id, KindAttachmentAdd, actor, a)
	if err != nil {
		return Attachment{}, e, err
	}
	a.Item, a.At, a.Actor = id, e.At, actor
	return a, e, nil
}

// TypedMediaType is the media type mt with the attachment type typ set as its
// type parameter (text/markdown; type=plan). Other parameters of mt are kept.
// It is empty when mt does not parse or typ is not a valid parameter value.
func TypedMediaType(mt, typ string) string {
	base, params, err := mime.ParseMediaType(mt)
	if err != nil {
		return ""
	}
	params["type"] = typ
	return mime.FormatMediaType(base, params)
}

// MediaTypeParam is the value of parameter key in media type mt; empty when mt
// does not parse or has no such parameter.
func MediaTypeParam(mt, key string) string {
	_, params, err := mime.ParseMediaType(mt)
	if err != nil {
		return ""
	}
	return params[key]
}
