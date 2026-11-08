package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/operator"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// The Awaiting-you page: every item in an operator state, the brief that was
// generated for it, whether the operator's word is recorded, and a form that
// records it. The record is satelled's one write, made through
// internal/operator (the owner the CLI's record verb uses too), from a
// loopback, same-origin browser POST. satelled never presents the move: the
// driving session (or the operator) presents the move the word names.

// briefType is the attachment type whose newest version is shown as an
// awaiting item's summary (media type parameter type=brief).
const briefType = "brief"

// operatorRowVM is one item awaiting the operator.
type operatorRowVM struct {
	ID, Kind, Title, Status, Workflow string
	Brief                             template.HTML
	BriefName                         string
	Moves                             []string
	// The operator's word, read from the repo's ledger (Read is false when the
	// ledger or workflow could not be read).
	Read     bool
	Recorded bool
	To       string
	Actor    string
	At       time.Time
}

// operatorPageData backs the Awaiting-you page.
type operatorPageData struct {
	TopBar      topBar
	ProjectName string
	Slug        string
	Rows        []operatorRowVM
	Recorded    string // the id just recorded (?recorded=)
	Note        string // why the operator's word could not be read
}

// leaves reports whether transition t leaves state s of w: it names s, or
// "*" and s is neither terminal nor park (the route's own rule).
func leaves(w *mirror.Workflow, t mirror.WorkflowTransition, s string) bool {
	if slices.Contains(t.From, s) {
		return true
	}
	if !slices.Contains(t.From, workflow.AnyState) {
		return false
	}
	st, _ := workflowState(w, s)
	return !st.Terminal && !st.Park
}

// movesFrom lists the states an item at state can move to in one declared
// move of w, in declaration order ("@prior" is left out: it resolves only for
// a parked item).
func movesFrom(w *mirror.Workflow, state string) []string {
	if w == nil {
		return nil
	}
	var out []string
	for _, t := range w.Transitions {
		if t.To == workflow.Prior || !leaves(w, t, state) || slices.Contains(out, t.To) {
			continue
		}
		out = append(out, t.To)
	}
	return out
}

// errNoLedger is a partition whose repo has no runtime ledger on this machine.
var errNoLedger = errors.New("this repo has no satelle ledger on this machine")

// operatorRepo opens repoRoot's ledger and workflows. It never creates a
// store: a missing ledger directory is errNoLedger.
func operatorRepo(repoRoot string) (*ledger.Store, map[string]*workflow.Workflow, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return nil, nil, errNoLedger
	}
	rt := config.RuntimeDir(repoRoot)
	if fi, err := os.Stat(filepath.Join(rt, "ledger")); err != nil || !fi.IsDir() {
		return nil, nil, errNoLedger
	}
	l, err := ledger.Open(rt)
	if err != nil {
		return nil, nil, err
	}
	wfs, err := workflow.Load(filepath.Join(repoRoot, config.DefaultDataDir, "workflows"))
	if err != nil {
		return nil, nil, err
	}
	return l, wfs, nil
}

// briefOf is the newest attachment of item id typed brief, from the pushed
// attachment rows; ok is false when there is none.
func briefOf(ctx context.Context, s *mirror.Store, repoKey, id string) (mirror.Attachment, bool) {
	rows, err := s.ListItems(ctx, repoKey, "story_doc")
	if err != nil {
		return mirror.Attachment{}, false
	}
	var best mirror.Attachment
	found := false
	for _, r := range rows {
		var a mirror.Attachment
		if json.Unmarshal([]byte(r.Payload), &a) != nil {
			continue
		}
		if a.Item == "" {
			a.Item, _, _ = strings.Cut(r.ID, "/")
		}
		if a.MediaType == "" {
			a.MediaType = a.Type
		}
		if a.Item != id || item.MediaTypeParam(a.MediaType, "type") != briefType {
			continue
		}
		if !found || a.At.After(best.At) {
			best, found = a, true
		}
	}
	return best, found
}

// operatorRows lists the partition's items in an operator state, oldest
// update first. With repoRoot set it reads each one's word from the ledger;
// note says why it could not.
func operatorRows(ctx context.Context, s *mirror.Store, repoKey, repoRoot string, withWord bool) (rows []operatorRowVM, note string, err error) {
	docs, err := decodeDocs(ctx, s, repoKey)
	if err != nil {
		return nil, "", err
	}
	workflows := workflowsOf(docs)
	var items []Item
	for _, kind := range []string{"story", "task", "execution"} {
		its, err := decodeItems(ctx, s, repoKey, kind)
		if err != nil {
			return nil, "", err
		}
		items = append(items, its...)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].UpdatedAt.Before(items[j].UpdatedAt) })
	for _, it := range items {
		w := workflows[it.Workflow]
		st, ok := workflowState(w, it.Status)
		if !ok || st.PerformerAgent != mirror.PerformerOperator {
			continue
		}
		vm := operatorRowVM{ID: it.ID, Kind: it.Kind, Title: it.Title, Status: it.Status, Workflow: it.Workflow, Moves: movesFrom(w, it.Status)}
		if b, ok := briefOf(ctx, s, repoKey, it.ID); ok {
			vm.BriefName = b.Name
			vm.Brief = renderMarkdown(stripDocFrontmatter(b.Body))
		}
		rows = append(rows, vm)
	}
	if !withWord || len(rows) == 0 {
		return rows, "", nil
	}
	l, wfs, err := operatorRepo(repoRoot)
	if err != nil {
		return rows, "the operator's word could not be read: " + err.Error(), nil
	}
	for i := range rows {
		w, ok := wfs[rows[i].Workflow]
		if !ok {
			continue
		}
		typ, ok := w.States[rows[i].Status].Operated()
		if !ok {
			continue
		}
		events, err := l.Read(rows[i].ID)
		if err != nil {
			continue
		}
		rows[i].Read = true
		if rec, ok := operator.Latest(events, typ); ok {
			rows[i].Recorded, rows[i].To, rows[i].Actor, rows[i].At = true, rec.To, rec.Actor, rec.At
		}
	}
	return rows, "", nil
}

// operatorPage is GET /r/{slug}/operator.
func (s *MirrorServer) operatorPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	repoKey, err := s.resolveSlug(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	id := mirrorIdentity(r.Context(), s.Store, repoKey)
	rows, note, err := operatorRows(r.Context(), s.Store, repoKey, id.RepoRoot, true)
	if err != nil {
		httpError(w, err)
		return
	}
	name := id.ProjectName
	if name == "" {
		name = slug
	}
	mirrorRender(w, "operatorPage", s.projectBase(slug), operatorPageData{
		TopBar:      newTopBar(r.Context(), s.Store, ""),
		ProjectName: name,
		Slug:        slug,
		Rows:        rows,
		Recorded:    r.URL.Query().Get("recorded"),
		Note:        note,
	})
}

// operatorRecord is POST /r/{slug}/operator-record/{id}: it records the
// operator's word on the item through internal/operator. It refuses, writing
// nothing, a request not from this machine, one that is not a same-origin
// browser form, any while satelled runs inside an agent session, and one
// whose retyped id differs.
func (s *MirrorServer) operatorRecord(w http.ResponseWriter, r *http.Request) {
	if !remoteIsLoopback(r.RemoteAddr) {
		http.Error(w, "the operator's word is accepted only from this machine (loopback)", http.StatusForbidden)
		return
	}
	if r.Header.Get("Sec-Fetch-Site") != "same-origin" || r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "the operator's word is accepted only from satelled's own page in a browser (same-origin form)", http.StatusForbidden)
		return
	}
	if err := operator.Session(os.Getenv("SATELLE_SESSION")); err != nil {
		http.Error(w, "satelled was started inside an agent session and records no operator's word; restart it from the operator's own terminal with satelle service restart ("+err.Error()+")", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if strings.TrimSpace(r.FormValue("retype")) != id {
		http.Error(w, "the id typed is not "+id+": nothing recorded", http.StatusBadRequest)
		return
	}
	slug := r.PathValue("slug")
	repoKey, err := s.resolveSlug(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ident := mirrorIdentity(r.Context(), s.Store, repoKey)
	l, wfs, err := operatorRepo(ident.RepoRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	actor := config.OSUser()
	if actor == "" {
		actor = "satelled"
	}
	if _, _, err := operator.Record(l, wfs, id, "", r.FormValue("to"), []byte(r.FormValue("word")), "text/markdown", actor); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.hub.publish("stories")
	http.Redirect(w, r, s.projectBase(slug)+"operator?recorded="+url.QueryEscape(id), http.StatusSeeOther)
}

const operatorTmplSrc = `
{{/* operatorRows: the project's Awaiting-you tab rows (fragment/operator). */}}
{{define "operatorRows"}}{{range .}}<tr class="row" tabindex="0" role="button" aria-expanded="false" data-kind="{{.Kind}}" data-status="{{.Status}}" data-workflow="{{.Workflow}}" data-title="{{lower .Title}}" data-search="{{printf "%s %s" .Title .ID | lower}}" data-expand-url="fragment/{{.Kind}}/{{.ID}}">
  <td class="id"><span class="id-copy" role="button" tabindex="0" data-id="{{.ID}}" title="Copy id to clipboard">{{.ID}}</span></td>
  <td><a class="wi-title" href="{{.Kind}}/{{.ID}}">{{.Title}}</a></td>
  <td>{{.Status}}</td>
  <td>{{if .BriefName}}{{.BriefName}}{{else}}<span class="meta">no brief</span>{{end}}</td>
  <td><a href="operator#op-{{.ID}}">decide →</a></td>
</tr>{{end}}{{end}}

{{define "operatorPage"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · awaiting you</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body data-page="operator">
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs"><a class="back-link" href="{{basehref}}#operator" aria-label="Back to project">←</a> <a href="{{basehref}}">{{.ProjectName}}</a> <span class="sep">/</span> <span class="cur">awaiting you</span></nav>
  <header class="app">
    <h1>awaiting you</h1>
    <div class="meta">{{len .Rows}} item{{if ne (len .Rows) 1}}s{{end}} in a state that is the operator's. Your word names the move; the driving session presents it.</div>
  </header>
  {{if .Recorded}}<p class="op-recorded" role="status">Recorded your word on {{.Recorded}}.</p>{{end}}
  {{if .Note}}<p class="meta">{{.Note}}</p>{{end}}
  {{range .Rows}}<section class="op-item" id="op-{{.ID}}">
    <h2><a href="{{.Kind}}/{{.ID}}">{{.ID}}</a> · {{.Title}}</h2>
    <div class="meta">{{.Kind}} · {{.Workflow}} · state {{.Status}}</div>
    <p class="op-word">{{if .Recorded}}{{.Actor}} chose {{.To}} at {{ftime .At}}; the driving session presents the move.{{else if .Read}}awaiting your word{{else}}the operator's word could not be read{{end}}</p>
    {{if .Brief}}<div class="prose op-brief">{{.Brief}}</div>{{else}}<p class="meta">No brief is attached.</p>{{end}}
    <form class="op-form" method="post" action="operator-record/{{.ID}}">
      <fieldset><legend>Move to</legend>{{$id := .ID}}{{range $i, $m := .Moves}}<label><input type="radio" name="to" value="{{$m}}" required{{if eq $i 0}} checked{{end}}> {{$m}}</label> {{end}}</fieldset>
      <label>Your word (a send-back says why)<br><textarea name="word" rows="4" cols="70" required></textarea></label><br>
      <label>Type the id to confirm: <input type="text" name="retype" autocomplete="off" required placeholder="{{$id}}"></label>
      <button type="submit">Record</button>
    </form>
  </section>{{else}}<div class="empty">Nothing awaits you.</div>{{end}}
  {{template "footer"}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}`
