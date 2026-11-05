package uipush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/push"
)

// Timeout bounds one push: a satelled that is not listening costs a refused
// connection, one that hangs costs no more than this.
const Timeout = 3 * time.Second

// ErrOtherInstance is returned when the endpoint serves another satelle home.
var ErrOtherInstance = errors.New("uipush: satelled at the endpoint serves another satelle home")

// Client posts to one satelled.
type Client struct {
	Endpoint string // satelled's base URL
	// InstanceID, when set, must equal the X-Satelle-Instance satelled's
	// /healthz answers, so a push never lands in another home's satelled.
	InstanceID string
	Origin     mirror.IngestOrigin
	HTTP       *http.Client // nil uses a client bounded by Timeout
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: Timeout}
}

func (c Client) url(path string) string {
	return strings.TrimRight(strings.TrimSpace(c.Endpoint), "/") + path
}

// check confirms the endpoint is up and serves this home.
func (c Client) check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/healthz"), nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("uipush: %s/healthz: %s", c.Endpoint, resp.Status)
	}
	if c.InstanceID != "" && resp.Header.Get("X-Satelle-Instance") != c.InstanceID {
		return ErrOtherInstance
	}
	return nil
}

// Seed pushes a full snapshot of src.
func (c Client) Seed(ctx context.Context, src Source) error {
	if err := c.check(ctx); err != nil {
		return err
	}
	snap, err := Full(src)
	if err != nil {
		return err
	}
	_, err = c.post(ctx, snap)
	return err
}

// Push reports a mutation of the items ids: a change event per topic, then a
// delta snapshot of those items; a partition satelled has not seeded gets a
// full snapshot instead. No ids pushes a full snapshot.
func (c Client) Push(ctx context.Context, src Source, ids []string) error {
	if err := c.check(ctx); err != nil {
		return err
	}
	p := &push.Publisher{Endpoint: c.Endpoint, RepoKey: src.RepoKey, Client: c.HTTP, Origin: string(c.Origin)}
	for _, topic := range topics(src, ids) {
		_ = p.PostContext(ctx, topic)
	}
	if len(ids) == 0 {
		snap, err := Full(src)
		if err != nil {
			return err
		}
		_, err = c.post(ctx, snap)
		return err
	}
	snap, err := Delta(src, ids)
	if err != nil {
		return err
	}
	code, err := c.post(ctx, snap)
	if code != http.StatusConflict {
		return err
	}
	full, ferr := Full(src)
	if ferr != nil {
		return ferr
	}
	_, err = c.post(ctx, full)
	return err
}

// topics are the coarse panels the items ids belong to.
func topics(src Source, ids []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, id := range ids {
		it, ok, err := src.Cache.Item(id)
		if err != nil || !ok {
			continue
		}
		if it.Kind == "task" || it.Kind == "execution" {
			add("tasks")
		} else {
			add("stories")
		}
	}
	if len(out) == 0 {
		add("stories")
	}
	return out
}

// post sends a snapshot and returns satelled's status code.
func (c Client) post(ctx context.Context, snap mirror.Snapshot) (int, error) {
	snap.Origin = c.Origin
	body, err := json.Marshal(snap)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/ingest/snapshot"), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("uipush: snapshot: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp.StatusCode, nil
}

// Remove drops the partition repoKey from satelled.
func (c Client) Remove(ctx context.Context, repoKey string) error {
	if err := c.check(ctx); err != nil {
		return err
	}
	body, _ := json.Marshal(mirror.RemoveEvent{RepoKey: repoKey})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/ingest/remove"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("uipush: remove: %s", resp.Status)
	}
	return nil
}
