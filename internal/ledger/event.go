package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type Event struct {
	ID      string          `json:"id"`
	Item    string          `json:"item"`
	Kind    string          `json:"kind"`
	At      time.Time       `json:"at"`
	Actor   string          `json:"actor"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Prev    string          `json:"prev"`
	Hash    string          `json:"hash"`
}

func Canonical(e Event) ([]byte, error) {
	var payload any
	if len(bytes.TrimSpace(e.Payload)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(e.Payload))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			return nil, fmt.Errorf("ledger: event %s payload: %w", e.ID, err)
		}
	}
	m := map[string]any{
		"id":      e.ID,
		"item":    e.Item,
		"kind":    e.Kind,
		"at":      e.At.UTC().Format(time.RFC3339Nano),
		"actor":   e.Actor,
		"payload": payload,
		"prev":    e.Prev,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func HashOf(e Event) (string, error) {
	b, err := Canonical(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func ShortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func (e Event) Decode(v any) error {
	if len(e.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(e.Payload, v)
}

type BrokenLink struct {
	Item   string
	Index  int
	ID     string
	Reason string
}

func (b *BrokenLink) Error() string {
	return fmt.Sprintf("ledger: %s event %d (%s): %s", b.Item, b.Index, b.ID, b.Reason)
}

func VerifyChain(item string, events []Event) error {
	prev := ""
	for i, e := range events {
		broken := func(why string) error { return &BrokenLink{Item: item, Index: i, ID: e.ID, Reason: why} }
		if e.Item != item {
			return broken(fmt.Sprintf("event names item %q", e.Item))
		}
		if e.Prev != prev {
			return broken(fmt.Sprintf("prev %q does not link to %q", e.Prev, prev))
		}
		h, err := HashOf(e)
		if err != nil {
			return broken(err.Error())
		}
		if h != e.Hash {
			return broken(fmt.Sprintf("hash %q does not match content (%q)", e.Hash, h))
		}
		prev = e.Hash
	}
	return nil
}
