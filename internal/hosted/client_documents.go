package hosted

// Workspace-scoped document store client (epic:scoped-sync, order:6). Mirrors
// the versioned config store's per-file surface: raw text/plain bytes, path-
// keyed under a workspace, with an incremental changed-since list for pull.
// Gemini/document conversion is deliberately out of scope — content is
// authored markdown bytes, byte-exact round-trip.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrDocumentFileMissing is returned when a document path does not exist in the
// workspace (HTTP 404 on GET).
var ErrDocumentFileMissing = errors.New("document file not found in workspace")

// ErrDocumentConflict is returned when a document push collides with server
// state the client must resolve (HTTP 409). Surfaces as a clear CLI message.
var ErrDocumentConflict = errors.New("document push conflict")

// DocumentChanges is the incremental list response: the items that changed
// since the caller's cursor, plus the new cursor to persist for the next pull.
// Item shape reuses ConfigItem (same path/version/sha/size/created_at fields).
type DocumentChanges struct {
	Items  []ConfigItem `json:"items"`
	Cursor string       `json:"cursor"`
}

// documentsManifestRoute is the project-addressed documents list/root
// (sty_ca64d0cb — team-workspaces Order 2).
func documentsManifestRoute(project string) string {
	return "/api/v1/projects/" + url.PathEscape(project) + "/documents"
}

// workspaceDocumentsRoute is the team workspace's documents list/root (the member
// route; sty_61872918). The argument is the workspace ID, not its name.
func workspaceDocumentsRoute(workspaceID string) string {
	return "/api/v1/workspaces/" + url.PathEscape(workspaceID) + "/documents"
}

// documentFileRoute is the project's per-path route.
func documentFileRoute(project, path string) string {
	return docFileRoute(documentsManifestRoute(project), path)
}

// nodeDocumentFileRoute is a remote node's per-path write route (satelle-server
// sty_8daf5634), authorised by the node token.
func nodeDocumentFileRoute(nodeID, path string) string {
	return docFileRoute("/api/v1/remote-nodes/"+url.PathEscape(nodeID)+"/documents", path)
}

// docFileRoute is the per-path route under a documents root, escaping each path
// segment so a path with odd characters round-trips while the slashes that
// structure it survive.
func docFileRoute(root, path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return root + "/" + strings.Join(segs, "/")
}

// ListDocumentChanges returns documents that changed after since (empty since =
// full set) for the project. The returned cursor is what the next pull should
// pass as since. A missing credential yields ErrLoginRequired; other non-200s a
// clean serverError.
func (c *Client) ListDocumentChanges(ctx context.Context, project, since string) (DocumentChanges, error) {
	return c.listDocumentChanges(ctx, documentsManifestRoute(project), since)
}

// ListWorkspaceDocumentChanges is ListDocumentChanges for a team workspace's
// collection, addressed by workspace ID.
func (c *Client) ListWorkspaceDocumentChanges(ctx context.Context, workspaceID, since string) (DocumentChanges, error) {
	return c.listDocumentChanges(ctx, workspaceDocumentsRoute(workspaceID), since)
}

func (c *Client) listDocumentChanges(ctx context.Context, root, since string) (DocumentChanges, error) {
	route := root
	if strings.TrimSpace(since) != "" {
		route += "?since=" + url.QueryEscape(since)
	}
	resp, err := c.doAuthed(ctx, http.MethodGet, route, nil, "")
	if err != nil {
		return DocumentChanges{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var out DocumentChanges
		if len(body) > 0 {
			if err := json.Unmarshal(body, &out); err != nil {
				return DocumentChanges{}, fmt.Errorf("hosted: decode document changes: %w", err)
			}
		}
		if out.Items == nil {
			out.Items = []ConfigItem{}
		}
		return out, nil
	case http.StatusUnauthorized:
		return DocumentChanges{}, ErrLoginRequired
	default:
		return DocumentChanges{}, fmt.Errorf("hosted: GET %s: %s", root, serverError(resp.StatusCode, body))
	}
}

// PushDocumentFile PUTs one file's verbatim bytes to
// /api/v1/projects/{project}/documents/{path}. Created distinguishes a new head
// (HTTP 201) from an idempotent re-push of the current head (HTTP 200). A 409
// yields ErrDocumentConflict; a missing credential yields ErrLoginRequired. A
// client made by ForNode PUTs to the node's documents route instead and ignores
// project, which the server reads from the node token.
func (c *Client) PushDocumentFile(ctx context.Context, project, path string, content []byte) (ConfigPushResult, error) {
	route := documentFileRoute(project, path)
	if c.nodeID != "" {
		route = nodeDocumentFileRoute(c.nodeID, path)
	}
	return c.putDocument(ctx, route, path, content)
}

// PushWorkspaceDocumentFile is PushDocumentFile to
// /api/v1/workspaces/{workspaceID}/documents/{path}. A non-2xx answer is an
// error naming the route and the status, never a success.
func (c *Client) PushWorkspaceDocumentFile(ctx context.Context, workspaceID, path string, content []byte) (ConfigPushResult, error) {
	return c.putDocument(ctx, docFileRoute(workspaceDocumentsRoute(workspaceID), path), path, content)
}

func (c *Client) putDocument(ctx context.Context, route, path string, content []byte) (ConfigPushResult, error) {
	if content == nil {
		content = []byte{}
	}
	resp, err := c.doAuthed(ctx, http.MethodPut, route, content, "text/plain; charset=utf-8")
	if err != nil {
		return ConfigPushResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var res ConfigPushResult
		if len(body) > 0 {
			if err := json.Unmarshal(body, &res); err != nil {
				return ConfigPushResult{}, fmt.Errorf("hosted: decode document push: %w", err)
			}
		}
		res.Path = path
		res.Created = resp.StatusCode == http.StatusCreated
		return res, nil
	case http.StatusConflict:
		return ConfigPushResult{}, ErrDocumentConflict
	case http.StatusUnauthorized:
		return ConfigPushResult{}, ErrLoginRequired
	default:
		return ConfigPushResult{}, fmt.Errorf("hosted: PUT %s: %s", route, serverError(resp.StatusCode, body))
	}
}

// DocumentFileContent GETs one path's content as text/plain from the project.
// The returned etag is the blob sha256 from the ETag header. A 404 yields
// ErrDocumentFileMissing.
func (c *Client) DocumentFileContent(ctx context.Context, project, path string) ([]byte, string, error) {
	return c.getDocument(ctx, documentFileRoute(project, path))
}

// WorkspaceDocumentFileContent is DocumentFileContent from a team workspace's
// collection, addressed by workspace ID.
func (c *Client) WorkspaceDocumentFileContent(ctx context.Context, workspaceID, path string) ([]byte, string, error) {
	return c.getDocument(ctx, docFileRoute(workspaceDocumentsRoute(workspaceID), path))
}

func (c *Client) getDocument(ctx context.Context, route string) ([]byte, string, error) {
	resp, err := c.doAuthed(ctx, http.MethodGet, route, nil, "")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return body, resp.Header.Get("ETag"), nil
	case http.StatusNotFound:
		return nil, "", ErrDocumentFileMissing
	case http.StatusUnauthorized:
		return nil, "", ErrLoginRequired
	default:
		return nil, "", fmt.Errorf("hosted: GET %s: %s", route, serverError(resp.StatusCode, body))
	}
}
