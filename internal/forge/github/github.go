// Package github is the GitHub provider behind the forge seam: the REST API's
// pull requests, reviews and commit statuses, called with a bearer token (on a
// remote node, the run's github-app installation token). It registers itself as
// "github"; nothing outside this package names it.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/forge"
)

// Name is the provider's [forge] provider value.
const Name = "github"

func init() {
	forge.Register(Name, func(cfg forge.Config) (forge.Forge, error) { return New(cfg) })
}

// DefaultHost is the GitHub a provider with no host calls.
const DefaultHost = "github.com"

// APIURL is the REST API base of a GitHub host: api.github.com for github.com,
// and the host's /api/v3 for an enterprise host.
func APIURL(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || strings.EqualFold(host, DefaultHost) {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

// Client calls one GitHub's REST API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New is a client for cfg: cfg.APIURL, else APIURL(cfg.Host).
func New(cfg forge.Config) (*Client, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("github: no token to call the API with")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.APIURL), "/")
	if base == "" {
		base = APIURL(cfg.Host)
	}
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: base, token: cfg.Token, http: hc}, nil
}

// pull is the part of a pull request the provider reads.
type pull struct {
	Number  int    `json:"number"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

func (p pull) pr(repo string) forge.PR {
	return forge.PR{Repo: repo, Number: p.Number, HeadRef: p.Head.Ref, HeadSHA: p.Head.SHA, Body: p.Body, URL: p.HTMLURL}
}

// PullRequestForHead lists the open PRs whose head is owner:head.
func (c *Client) PullRequestForHead(ctx context.Context, repo, head string) (forge.PR, error) {
	owner, _, _ := strings.Cut(repo, "/")
	q := url.Values{"state": {"open"}, "head": {owner + ":" + head}}
	var pulls []pull
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/pulls?"+q.Encode(), "", nil, &pulls); err != nil {
		return forge.PR{}, err
	}
	var match []pull
	for _, p := range pulls {
		if p.Head.Ref == head { // the API filters by head; a stray answer is not trusted
			match = append(match, p)
		}
	}
	switch len(match) {
	case 0:
		return forge.PR{}, forge.ErrNoPR
	case 1:
		return match[0].pr(repo), nil
	}
	nums := make([]int, len(match))
	for i, p := range match {
		nums[i] = p.Number
	}
	return forge.PR{}, &forge.ManyPRsError{Head: head, Numbers: nums}
}

// PullRequest reads PR n.
func (c *Client) PullRequest(ctx context.Context, repo string, n int) (forge.PR, error) {
	var p pull
	if err := c.do(ctx, http.MethodGet, pullPath(repo, n), "", nil, &p); err != nil {
		return forge.PR{}, err
	}
	return p.pr(repo), nil
}

// Diff reads PR n in GitHub's diff media type.
func (c *Client) Diff(ctx context.Context, repo string, n int) (string, error) {
	var b bytes.Buffer
	if err := c.do(ctx, http.MethodGet, pullPath(repo, n), "application/vnd.github.diff", nil, &b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Approve re-reads PR n's head and, only when it is still sha, records an
// APPROVE review on commit sha.
func (c *Client) Approve(ctx context.Context, repo string, n int, sha, body string) (forge.Review, error) {
	now, err := c.PullRequest(ctx, repo, n)
	if err != nil {
		return forge.Review{}, err
	}
	if now.HeadSHA != sha {
		return forge.Review{}, &forge.HeadMovedError{Reviewed: sha, Now: now.HeadSHA}
	}
	var out struct {
		ID       int64  `json:"id"`
		CommitID string `json:"commit_id"`
		State    string `json:"state"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	in := map[string]string{"commit_id": sha, "event": "APPROVE", "body": body}
	if err := c.do(ctx, http.MethodPost, pullPath(repo, n)+"/reviews", "", in, &out); err != nil {
		return forge.Review{}, err
	}
	return forge.Review{ID: out.ID, Author: out.User.Login, CommitID: out.CommitID, State: out.State}, nil
}

// SetStatus posts s on commit sha.
func (c *Client) SetStatus(ctx context.Context, repo, sha string, s forge.Status) error {
	in := map[string]string{"state": s.State, "context": s.Context, "description": s.Description}
	return c.do(ctx, http.MethodPost, "/repos/"+repo+"/statuses/"+sha, "", in, nil)
}

func pullPath(repo string, n int) string { return "/repos/" + repo + "/pulls/" + strconv.Itoa(n) }

// APIError is a call GitHub answered with a status other than 2xx. Its text
// carries the method, path, status and GitHub's message, never the token.
type APIError struct {
	Method, Path string
	Status       int
	Message      string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github: %s %s: %d %s", e.Method, e.Path, e.Status, e.Message)
}

// do calls the API. in, when set, is sent as JSON; out is decoded from JSON, or
// copied as is when it is a *bytes.Buffer. accept replaces the JSON media type.
func (c *Client) do(ctx context.Context, method, path, accept string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &msg)
		if msg.Message == "" {
			msg.Message = http.StatusText(resp.StatusCode)
		}
		p, _, _ := strings.Cut(path, "?")
		return &APIError{Method: method, Path: p, Status: resp.StatusCode, Message: msg.Message}
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *bytes.Buffer:
		_, err := o.Write(raw)
		return err
	default:
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("github: %s %s: decode: %w", method, path, err)
		}
		return nil
	}
}
