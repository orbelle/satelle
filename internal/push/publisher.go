package push

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const DefaultTimeout = 500 * time.Millisecond

type Event struct {
	RepoKey string `json:"repo_key"`
	Topic   string `json:"topic"`
	Entity  string `json:"entity"`
	At      string `json:"at"`
	Origin  string `json:"origin,omitempty"`
}

type Publisher struct {
	Endpoint string
	RepoKey  string
	Client   *http.Client
	Path     string
	Now      func() time.Time
	OnPost   func(*http.Request)
	Origin   string
}

func (p *Publisher) Active() bool {
	return p != nil && strings.TrimSpace(p.Endpoint) != ""
}

func (p *Publisher) Notify(topic string) {
	if !p.Active() {
		return
	}
	go func() { _ = p.PostContext(context.Background(), topic) }()
}

func (p *Publisher) PostContext(ctx context.Context, topic string) (err error) {
	if !p.Active() {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = nil
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	endpoint := strings.TrimRight(strings.TrimSpace(p.Endpoint), "/")
	path := p.Path
	if path == "" {
		path = "/ingest/change"
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	ev := Event{
		RepoKey: p.RepoKey,
		Topic:   topic,
		Entity:  entityForTopic(topic),
		At:      now().UTC().Format(time.RFC3339Nano),
		Origin:  p.Origin,
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.OnPost != nil {
		p.OnPost(req)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func entityForTopic(topic string) string {
	switch topic {
	case "stories":
		return "story"
	case "tasks":
		return "task"
	case "docs":
		return "doc"
	default:
		return topic
	}
}
