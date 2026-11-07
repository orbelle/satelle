package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/help"
	"github.com/bobmcallan/satelle/internal/mirror"
)

// MirrorServer is the push-fed read-only UI (sty_dbdadfa0 + epic:mirror-ui-parity).
// It never opens a per-repo runtime DB; all product state comes from the mirror store.
type MirrorServer struct {
	Handler http.Handler
	Store   *mirror.Store
	// InstanceID is exposed on GET /healthz as X-Satelle-Instance so CLI
	// auto-bootstrap only seeds a serve that matches the caller's SATELLE_HOME
	// (sty_5aa08259 / epic:mirror-hygiene).
	InstanceID string
	// LogPath is the satelled server.log shown next to a standing push failure.
	LogPath string
	// Ingest is the mounted ingest handler; satelled sets its Log before serving.
	Ingest *mirror.IngestHandler
	hub    *hub
}

// partitionVM is one project row on the mirror landing. Counts mirror the
// project page tabs: Stories, Tasks, Workflow.
type partitionVM struct {
	Slug      string
	Name      string
	Path      string
	Stories   int
	Tasks     int
	Workflows int
	// LastIngest carries the same freshness signal as the project page, so a
	// partition the serving tier could not reconcile is visible on the landing
	// too (sty_e6e467fe) — as elapsed time on every row (sty_226a661e), plus the
	// stale marker on a row past mirror.StaleAfter.
	LastIngest time.Time
	Stale      bool
	// SyncLastSuccess / SyncReason are recorded hosted-push state
	// (sty_30696eeb). Empty reason = no standing failure.
	SyncLastSuccess time.Time
	SyncReason      string
	SyncLocal       bool
	SyncLogPath     string
}

// mirrorProjectsData backs the Projects landing template.
type mirrorProjectsData struct {
	Partitions   []partitionVM
	TotalStories int
	TopBar       topBar
	Empty        bool
}

// mirrorTmpl is a clone-source that is NEVER executed on the root value — only
// Clone()+ExecuteTemplate, so concurrent request-local basehref
// Func works (html/template forbids Clone after Execute on the same *Template).
var mirrorTmpl *template.Template

func init() {
	// Full template set (shared page chrome + the Projects landing).
	mirrorTmpl = template.Must(template.New("web").Funcs(tmplFuncs).Parse(templatesSrc))
	template.Must(mirrorTmpl.New("mirrorProjects").Parse(mirrorProjectsSrc))
}

// NewMirror builds the RO+ingest HTTP surface over m. Instance identity is
// derived from the process GlobalDir (same home as the mirror DB path).
func NewMirror(m *mirror.Store) *MirrorServer {
	return NewMirrorWithInstance(m, config.SafeCurrentInstanceID())
}

// NewMirrorWithInstance is like NewMirror but sets an explicit instance id
// (tests inject a fixed id; production uses CurrentInstanceID).
func NewMirrorWithInstance(m *mirror.Store, instanceID string) *MirrorServer {
	return NewMirrorHooks(m, instanceID, nil)
}

// NewMirrorHooks is NewMirrorWithInstance plus an optional OnIngest hook
// (satelled hosted push; sty_c526753a). Tests and NewMirror pass nil.
func NewMirrorHooks(m *mirror.Store, instanceID string, onIngest func(string, mirror.IngestOrigin)) *MirrorServer {
	serverStart = time.Now()
	h := newHub()
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		b, err := staticFS.ReadFile("static/favicon.svg")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(b)
	})

	s := &MirrorServer{Store: m, InstanceID: instanceID, hub: h, LogPath: displayedSyncLogPath()}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.InstanceID != "" {
			w.Header().Set("X-Satelle-Instance", s.InstanceID)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
			buildinfo.Info
		}{"ok", buildinfo.Resolve()})
	})
	mux.HandleFunc("GET /events", h.serveEvents)
	mux.HandleFunc("GET /theme", getTheme)
	// POST /theme removed (only ingest mutates). Theme persists client-side.

	s.Ingest = &mirror.IngestHandler{Store: m, OnChange: h.publish, OnIngest: onIngest}
	s.Ingest.Mount(mux)

	// Projects landing (order:3) and project surface under /r/{slug}/ (order:2).
	mux.HandleFunc("GET /{$}", s.landing)
	// Live-region fragment for the landing: app.js soft-refreshes counts (and
	// rows when the served set changes) without a full-page reload flash.
	mux.HandleFunc("GET /fragment/projects", s.landingFragment)
	mux.HandleFunc("GET /r/{slug}/{$}", s.projectHome)
	mux.HandleFunc("GET /r/{slug}/fragment/stories", s.fragmentRows("workitemRows", "stories"))
	mux.HandleFunc("GET /r/{slug}/fragment/tasks", s.fragmentRows("workitemRows", "tasks"))
	mux.HandleFunc("GET /r/{slug}/fragment/queues", s.fragmentRows("queueRows", "queues"))
	mux.HandleFunc("GET /r/{slug}/fragment/activity", s.fragmentRows("activityRows", "activity"))
	mux.HandleFunc("GET /r/{slug}/fragment/docs", s.fragmentRows("docsRows", "docs"))
	mux.HandleFunc("GET /r/{slug}/fragment/engagement", s.fragmentBadge("engagementBadge"))
	mux.HandleFunc("GET /r/{slug}/fragment/backlog", s.fragmentBadge("backlogBadge"))
	mux.HandleFunc("GET /r/{slug}/fragment/story/{id}", s.itemFragment("story"))
	mux.HandleFunc("GET /r/{slug}/fragment/task/{id}", s.itemFragment("task"))
	mux.HandleFunc("GET /r/{slug}/fragment/execution/{id}", s.itemFragment("execution"))
	mux.HandleFunc("GET /r/{slug}/fragment/queue/{id}", s.itemFragment("queue"))
	mux.HandleFunc("GET /r/{slug}/fragment/workflow/{name}", s.workflowFragment)
	mux.HandleFunc("GET /r/{slug}/story/{id}", s.itemDetail("story"))
	mux.HandleFunc("GET /r/{slug}/task/{id}", s.itemDetail("task"))
	mux.HandleFunc("GET /r/{slug}/execution/{id}", s.itemDetail("execution"))
	mux.HandleFunc("GET /r/{slug}/queue/{id}", s.itemDetail("queue"))
	mux.HandleFunc("GET /r/{slug}/doc/{kind}/{name}", s.docPage)
	mux.HandleFunc("GET /r/{slug}/help", s.helpPage)
	mux.HandleFunc("GET /r/{slug}/settings", s.settingsPage)
	mux.HandleFunc("GET /settings/global", s.globalSettingsPage)
	mux.HandleFunc("POST /settings/global", s.globalSettingsSave)
	mux.HandleFunc("GET /logs", s.logsPage)
	mux.HandleFunc("GET /fragment/logs", s.logsFragment)
	// oauth stays unmounted; product state is still RO.

	s.Handler = mux
	return s
}

// Publish rings SSE (ingest already does this via OnChange).
func (s *MirrorServer) Publish(topic string) { s.hub.publish(topic) }

func (s *MirrorServer) resolveSlug(ctx context.Context, slug string) (string, error) {
	parts, err := s.Store.ListPartitions(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range parts {
		if p.Slug == slug || p.RepoKey == slug {
			return p.RepoKey, nil
		}
	}
	return "", fmt.Errorf("unknown slug %q", slug)
}

// displaySlugs maps each partition's repo_key to the URL slug used on the
// landing and in crumbs. When multiple partitions share the same directory
// basename slug (legacy dirty mirrors), each collision falls back to its
// unique repo_key so hrefs never collide (sty_57d5ce25 AC2). Empty stored
// slugs also fall back to repo_key.
func displaySlugs(parts []mirror.Partition) map[string]string {
	count := map[string]int{}
	for _, p := range parts {
		s := strings.TrimSpace(p.Slug)
		if s == "" {
			continue
		}
		count[s]++
	}
	out := make(map[string]string, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p.Slug)
		if s == "" || count[s] > 1 {
			out[p.RepoKey] = p.RepoKey
			continue
		}
		out[p.RepoKey] = s
	}
	return out
}

func (s *MirrorServer) projectBase(slug string) string {
	return "/r/" + slug + "/"
}

// mirrorRender executes a named template with a request-local basehref.
func mirrorRender(w http.ResponseWriter, name, base string, data any) {
	t, err := mirrorTmpl.Clone()
	if err != nil {
		httpError(w, err)
		return
	}
	t = t.Funcs(template.FuncMap{
		"basehref": func() string { return base },
	})
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// loadPartitions builds the landing row set. Counts match project-page tabs.
func (s *MirrorServer) loadPartitions(ctx context.Context) ([]partitionVM, int, error) {
	parts, err := s.Store.ListPartitions(ctx)
	if err != nil {
		return nil, 0, err
	}
	slugs := displaySlugs(parts)
	var pvm []partitionVM
	total := 0
	for _, p := range parts {
		slug := slugs[p.RepoKey]
		id := mirrorIdentity(ctx, s.Store, p.RepoKey)
		name := id.ProjectName
		if name == "" {
			name = slug
		}
		path := id.RepoRoot
		if path == "" {
			path = p.RepoKey
		}
		stories, _ := decodeItems(ctx, s.Store, p.RepoKey, "story")
		tasks, _ := decodeItems(ctx, s.Store, p.RepoKey, "task")
		docs, _ := decodeDocs(ctx, s.Store, p.RepoKey)
		workflows := len(workflowRows(docs))
		total += len(stories)
		lastIngest, _ := p.LastIngest()
		st := loadPartitionSync(path)
		logPath := ""
		if st.PushReason != "" {
			logPath = s.LogPath
		}
		pvm = append(pvm, partitionVM{
			Slug: slug, Name: name, Path: path,
			Stories: len(stories), Tasks: len(tasks), Workflows: workflows,
			LastIngest:      lastIngest.Local(),
			Stale:           p.Stale(time.Now()),
			SyncLastSuccess: st.PushLastSuccess,
			SyncReason:      st.PushReason,
			SyncLocal:       st.Scope == "local",
			SyncLogPath:     logPath,
		})
	}
	return pvm, total, nil
}

// landing renders this machine's projects over mirror partitions (order:3).
func (s *MirrorServer) landing(w http.ResponseWriter, r *http.Request) {
	pvm, total, err := s.loadPartitions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mirrorRender(w, "mirrorProjects", "/", mirrorProjectsData{
		Partitions:   pvm,
		TotalStories: total,
		TopBar:       newTopBar(r.Context(), s.Store, "projects"),
		Empty:        len(pvm) == 0,
	})
}

// landingFragment returns the projects live region (rows or empty) for realtime.
func (s *MirrorServer) landingFragment(w http.ResponseWriter, r *http.Request) {
	pvm, _, err := s.loadPartitions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mirrorRender(w, "mirrorProjectsLive", "/", mirrorProjectsData{
		Partitions: pvm,
		Empty:      len(pvm) == 0,
	})
}

func (s *MirrorServer) projectHome(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	repoKey, err := s.resolveSlug(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, _, err := mirrorLoadPanels(r.Context(), s.Store, repoKey, slug)
	if err != nil {
		httpError(w, err)
		return
	}
	data.Projects = s.crumbProjects(r.Context(), slug)
	mirrorRender(w, "page", s.projectBase(slug), data)
}

func (s *MirrorServer) crumbProjects(ctx context.Context, currentSlug string) []crumbProject {
	parts, err := s.Store.ListPartitions(ctx)
	if err != nil || len(parts) < 2 {
		return nil
	}
	slugs := displaySlugs(parts)
	nameCount := map[string]int{}
	type pair struct{ slug, name, path string }
	var list []pair
	for _, p := range parts {
		slug := slugs[p.RepoKey]
		id := mirrorIdentity(ctx, s.Store, p.RepoKey)
		name := id.ProjectName
		if name == "" {
			name = slug
		}
		path := id.RepoRoot
		if path == "" {
			path = p.RepoKey
		}
		nameCount[name]++
		list = append(list, pair{slug, name, path})
	}
	out := make([]crumbProject, 0, len(list))
	for _, p := range list {
		// Template href="/{{.Slug}}/" — prefix with r/ for mirror paths.
		out = append(out, crumbProject{
			Name:      p.name,
			Slug:      "r/" + p.slug,
			Path:      p.path,
			Current:   p.slug == currentSlug,
			Ambiguous: nameCount[p.name] > 1,
		})
	}
	return out
}

func (s *MirrorServer) fragmentRows(tmplName, topic string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		repoKey, err := s.resolveSlug(r.Context(), slug)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data, _, err := mirrorLoadPanels(r.Context(), s.Store, repoKey, slug)
		if err != nil {
			httpError(w, err)
			return
		}
		base := s.projectBase(slug)
		var payload any
		switch topic {
		case "stories":
			payload = data.Stories
		case "tasks":
			payload = data.Tasks
		case "queues":
			payload = data.Queues
		case "activity":
			payload = data.Activity
		case "docs":
			payload = data.DocKinds
		default:
			http.NotFound(w, r)
			return
		}
		mirrorRender(w, tmplName, base, payload)
	}
}

// fragmentBadge returns a project-chrome badge template's HTML — the engagement
// or backlog chip — which is empty when its count is 0 (sty_e4632f45).
func (s *MirrorServer) fragmentBadge(tmplName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		repoKey, err := s.resolveSlug(r.Context(), slug)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data, _, err := mirrorLoadPanels(r.Context(), s.Store, repoKey, slug)
		if err != nil {
			httpError(w, err)
			return
		}
		mirrorRender(w, tmplName, s.projectBase(slug), data)
	}
}

func (s *MirrorServer) itemFragment(group string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		id := r.PathValue("id")
		repoKey, err := s.resolveSlug(r.Context(), slug)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		d, _, err := mirrorLoadDetail(r.Context(), s.Store, repoKey, group, id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mirrorRender(w, "itemDetail", s.projectBase(slug), d)
	}
}

func (s *MirrorServer) itemDetail(group string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		id := r.PathValue("id")
		repoKey, err := s.resolveSlug(r.Context(), slug)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		d, _, err := mirrorLoadDetail(r.Context(), s.Store, repoKey, group, id)
		if err != nil {
			http.Error(w, "not found: "+id, http.StatusNotFound)
			return
		}
		d.Standalone = true
		mirrorRender(w, "detailPage", s.projectBase(slug), d)
	}
}

// workflowFragment is a Workflow panel row's expansion: the workflow's route
// as pushed, current state none.
func (s *MirrorServer) workflowFragment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	repoKey, err := s.resolveSlug(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	doc, err := findDoc(r.Context(), s.Store, repoKey, mirror.DocKindWorkflows, r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	route := buildRoute(doc.Workflow, doc.Name, "", nil)
	mirrorRender(w, "workflowDetail", s.projectBase(slug), docPageData{Kind: doc.Kind, Name: doc.Name, Source: doc.Source, Workflow: &route})
}

// docPage renders one pushed doc. Repo documents are a removed feature: a doc
// the partition no longer holds answers 404; a workflow doc draws its route.
func (s *MirrorServer) docPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	kind, name := r.PathValue("kind"), r.PathValue("name")
	repoKey, err := s.resolveSlug(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	doc, err := findDoc(r.Context(), s.Store, repoKey, kind, name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data := docPageData{
		TopBar:   newTopBar(r.Context(), s.Store, ""),
		Kind:     kind,
		Name:     doc.Name,
		Headline: doc.Headline,
		Source:   doc.Source,
	}
	if doc.Workflow != nil {
		route := buildRoute(doc.Workflow, doc.Name, "", nil)
		data.Workflow = &route
		data.HTML = template.HTML("<pre class=\"prose\">" + template.HTMLEscapeString(doc.Body) + "</pre>")
	} else {
		data.HTML = renderMarkdown(doc.Body)
	}
	mirrorRender(w, "docPage", s.projectBase(slug), data)
}

func (s *MirrorServer) helpPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, err := s.resolveSlug(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	topics := make([]helpTopic, 0)
	for _, t := range help.List() {
		topics = append(topics, helpTopic{
			Name:  t.Name,
			Title: t.Title,
			HTML:  renderMarkdown(stripLeadingHeading(t.Body)),
		})
	}
	mirrorRender(w, "help", s.projectBase(slug), helpPageData{
		Topics: topics,
		TopBar: newTopBar(r.Context(), s.Store, "help"),
	})
}

func (s *MirrorServer) settingsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	repoKey, err := s.resolveSlug(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, _, err := mirrorSettingsData(r.Context(), s.Store, repoKey)
	if err != nil {
		httpError(w, err)
		return
	}
	mirrorRender(w, "settings", s.projectBase(slug), data)
}

type globalSettingsData struct {
	Saved  bool
	Server string
	Theme  string
	TopBar topBar
}

func (s *MirrorServer) globalSettingsPage(w http.ResponseWriter, r *http.Request) {
	gc, _ := config.LoadGlobal()
	mirrorRender(w, "globalSettings", "/", globalSettingsData{
		Saved:  r.URL.Query().Get("saved") == "1",
		Server: gc.Hosted.Server,
		Theme:  gc.UI.Theme,
		TopBar: newTopBar(r.Context(), s.Store, "projects"),
	})
}

func (s *MirrorServer) globalSettingsSave(w http.ResponseWriter, r *http.Request) {
	if !remoteIsLoopback(r.RemoteAddr) {
		http.Error(w, "writes are accepted only from this machine (loopback)", http.StatusForbidden)
		return
	}
	// FormValue parses urlencoded or multipart (the page's FormData submit).
	// Do not call ParseForm first — that marks the form parsed and leaves
	// multipart fields empty.
	server := strings.TrimSpace(r.FormValue("server"))
	if server == "" {
		if err := config.ClearGlobalHostedServer(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := validateHostedServerURL(server); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := config.SaveGlobalHostedServer(server); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if theme := strings.TrimSpace(r.FormValue("theme")); theme == "light" || theme == "dark" {
		gc, err := config.LoadGlobal()
		if err == nil {
			gc.UI.Theme = theme
			_ = config.SaveGlobal(gc)
		}
	}
	if r.Header.Get("X-Satelle-Settings") != "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings/global?saved=1", http.StatusSeeOther)
}

func remoteIsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateHostedServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("hosted server must be an absolute http(s) URL")
	}
	return nil
}

// MirrorDefaultPath is a convenience for tests.
func MirrorDefaultPath() string {
	return mirror.DefaultPath(config.GlobalDir())
}

// Silence unused buildinfo if footer version comes from template func only.
var _ = buildinfo.Version

const mirrorProjectsSrc = `
{{/* mirrorProjectsLive: the landing live region — full page embeds it; GET
     /fragment/projects returns only this so app.js can soft-refresh counts. */}}
{{define "mirrorProjectsLive"}}{{if .Empty}}
  <div class="empty">No partitions yet — run <code>satelle project add</code> to seed the mirror (satelled listens at the machine <code>[service] endpoint</code>).</div>
  {{else}}
  <table class="panel-table">
    <thead><tr><th>Project</th><th>Path</th><th>Stories</th><th>Tasks</th><th>Workflow</th><th>Updated</th></tr></thead>
    <tbody data-rows>{{range .Partitions}}
      <tr class="row" data-slug="{{.Slug}}">
        {{/* row-link + CSS ::after stretch the name <a> across the row (sty_be23ae50). */}}
        <td><a class="wi-title row-link" href="/r/{{.Slug}}/">{{.Name}}</a></td>
        <td class="meta mono">{{.Path}}</td>
        <td class="n-stories"><span class="n">{{.Stories}}</span></td>
        <td class="n-tasks"><span class="n">{{.Tasks}}</span></td>
        <td class="n-workflows"><span class="n">{{.Workflows}}</span></td>
        {{/* Rightmost deliberately: this replaces a badge that sat INSIDE the
             Project cell and pushed the name around (sty_226a661e). Landing
             shows local ingest only; a standing fail is a tag, not a log
             dump (sty_a447695e). */}}
        <td class="updated-cell">{{template "freshnessBare" .}}{{template "stalemark" .}}{{template "syncfailtag" .}}</td>
      </tr>{{end}}
    </tbody>
  </table>
  {{end}}{{end}}

{{define "mirrorProjects"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · projects</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body data-page="projects">
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs">{{template "rootcrumb"}}</nav>
  <header class="app">
    <h1>projects</h1>
    <div class="meta"><span class="n-partitions">{{len .Partitions}}</span> partition{{if ne (len .Partitions) 1}}s{{end}} · push-fed mirror</div>
  </header>
  <div id="projects-live">{{template "mirrorProjectsLive" .}}</div>
  {{template "footer"}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}`
