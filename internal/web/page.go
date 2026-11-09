package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// basePath is the URL prefix this server is mounted under, empty for the
// supervisor (which serves the / landing and shared chrome) and "/<slug>" for a
// project served behind the supervisor's reverse proxy. It is a process global
// because each project is its own process.
var basePath string

// SetBasePath sets the mount prefix (trailing slash trimmed). Call before New.
func SetBasePath(p string) {
	basePath = "/" + strings.Trim(p, "/")
	if basePath == "/" {
		basePath = ""
	}
}

// baseHref returns the value for the page's <base href> — always slash-terminated
// so relative URLs in app.js resolve under the mount: "/" at root, "/slug/" under
// the proxy.
func baseHref() string {
	if basePath == "" {
		return "/"
	}
	return basePath + "/"
}

// tmplFuncs are shared template helpers.
var tmplFuncs = template.FuncMap{
	"basehref": baseHref,
	"ftime": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Local().Format("2006-01-02 15:04")
	},
	// isotime is the machine-readable half of a freshness stamp: the client
	// ticker re-renders the phrase from THIS, so it must be an absolute instant
	// rather than a formatted local string. Zero renders empty, which is the
	// ticker's signal to leave that element alone.
	"isotime": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	},
	"reltime": func(t time.Time) string { return relTime(t, time.Now()) },
	"lower":   strings.ToLower,
	"join": func(ss []string, sep string) string {
		return strings.Join(ss, sep)
	},
	// users is the assignees' users joined by commas: a row's data-assignee
	// filter value and its search text.
	"users": func(as []assigneeVM) string {
		us := make([]string, len(as))
		for i, a := range as {
			us[i] = a.User
		}
		return strings.Join(us, ",")
	},
	// evdot maps a timeline event to its dot's outcome class from the gate
	// result in the payload: a reject is a fail dot, an error (a judge that
	// could not be asked) an error dot, an accept or carry a pass dot; every
	// other event keeps the default dot.
	"evdot": func(ev eventVM) string {
		for _, c := range ev.Chips {
			if c.Type != "outcome" {
				continue
			}
			if c.Label == mirror.GateReject {
				return "tl-fail"
			}
			if c.Label == mirror.GateError {
				return "tl-error"
			}
			if c.Label == gateAccept || c.Label == gateCarried {
				return "tl-pass"
			}
		}
		return ""
	},
	// dict builds a map from alternating keys and values, for passing a small
	// view to a shared template.
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[fmt.Sprint(kv[i])] = kv[i+1]
		}
		return m
	},
	// tagchip renders a tag chip. A key:value tag (e.g. epic:summariser) renders
	// as a kv chip distinguishing key from value; a bare tag renders plain. No
	// schema change — kv is a parsed string convention.
	//
	// A chip that maps to a real filter facet renders as a <button> carrying the
	// exact filter token it adds when clicked (app.js wires it to the panel's
	// filter input): a category:<v> chip filters the category facet; every other
	// tag filters the tags facet (tags:<full-tag>). scope:/applies_to: chips are
	// workflow metadata, not filter facets, so they stay inert spans.
	"tagchip": func(tag string) template.HTML {
		esc := template.HTMLEscapeString
		cls := "tagchip"
		inner := esc(tag)
		key := ""
		if i := strings.IndexByte(tag, ':'); i > 0 && i < len(tag)-1 {
			key = tag[:i]
			cls = "tagchip kv"
			if key == "category" { // category gets a distinct key colour, like satellites
				cls += " cat"
			}
			inner = `<span class="k">` + esc(key) + `</span><span class="v">` + esc(tag[i+1:]) + `</span>`
		}
		token := ""
		switch key {
		case "scope", "applies_to":
			// inert: workflow metadata, not a filter facet
		case "category", "task":
			token = tag // category:<value> and an execution's task:<id> are themselves filter tokens
		default:
			token = "tags:" + tag
		}
		if token == "" {
			return template.HTML(`<span class="` + cls + `">` + inner + `</span>`)
		}
		return template.HTML(`<button type="button" class="` + cls + ` clickable" data-filter="` +
			esc(token) + `" aria-label="filter by ` + esc(tag) + `">` + inner + `</button>`)
	},
	// tabof maps a work-item kind to its panel/tab name (story→stories). Takes
	// any so the workitem.Kind type (a distinct string type) is accepted.
	"tabof": func(kind any) string {
		switch fmt.Sprint(kind) {
		case "task":
			return "tasks"
		case "execution":
			return "activity"
		case "queue":
			return "queues"
		}
		return "stories"
	},
}

// Gate results that pass (gate.Accept, gate.Carried).
const (
	gateAccept  = "accept"
	gateCarried = "carried"
)

// relTime renders an elapsed duration as the phrase shown in a freshness stamp
// (sty_226a661e). Buckets, thresholds and plural forms are MIRRORED in
// relPhrase() in static/app.js, which re-renders the same element on a timer —
// so the two must agree exactly or the text visibly re-words on the first tick
// with no time elapsed. Edit one, edit the other; TestRelTimeBuckets pins the
// Go side and TestAppJSMirrorsRelTimeWording fences the JS side.
//
// Floor arithmetic throughout: "1 hr ago" holds for the whole hour, so a client
// tick lands on the same string the server produced.
func relTime(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < 0 {
		// Clock skew between the ingesting repo and the serving tier. "in 3
		// minutes" reads as a bug; treat any future stamp as current.
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "min")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hr")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

// plural renders "1 hr ago" / "3 hrs ago" — the one place the -s is decided, so
// the Go and JS sides cannot disagree about it independently.
func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

// tmpl is the whole page's template set: the full page, the per-panel row
// fragments (reused by the realtime refetch), and the inline item detail
// (reused by the expand fragment and the standalone detail page).
var tmpl = template.Must(template.New("web").Funcs(tmplFuncs).Parse(templatesSrc))

const templatesSrc = `
{{/* topbar: the ONE shared navbar, a full-bleed band placed directly inside <body>,
     above every page's .wrap. Matches the LIVE satelle.dev header (sty_2faa7dd4):
     the ◐ satelle mark LEADS at the left (the ◐ is accent, the wordmark is ink); a
     right-aligned nav row of TEXT links (Install · Docs · Projects · Logs) —
     Install/Docs open satelle.dev in a new tab, Projects is this machine's projects
     landing, Logs is the machine-wide server.log (sty_df4a0785); active via
     .Active — then a GitHub OUTLINED ICON button (new tab, not a text link);
     the account control; and the theme toggle LAST. There are no Home/Help top-nav
     items — the mark IS the home affordance and Help stays on the meta/breadcrumb
     line. The mark folds in two signals: the uptime snapshot rides in its title
     tooltip, and the live /events (SSE) connection state is its colour — accent when
     connected, muted --fail-soft on the ◐ only (.sse-down, added by app.js) when the
     stream drops. The theme toggle glyph is ☾/☀ (app.js), never ◐. Mobile-collapsible
     nav is out of scope — the row stays inline. */}}
{{define "topbar"}}<header class="topbar"><div class="topbar-inner"><a class="brand-mark" href="https://satelle.dev/" target="_blank" rel="noopener" title="satelle — home{{if .Uptime}} · {{.Uptime}} at page load · mark colour = live-update connection{{end}}" aria-label="satelle home (opens in a new tab)">{{template "brandmark-svg"}}<span class="brand-word">satelle</span></a><div class="topbar-controls"><nav class="topnav"><a href="https://satelle.dev/install" target="_blank" rel="noopener">Install</a><a href="https://satelle.dev/docs" target="_blank" rel="noopener">Docs</a><a href="/"{{if eq .Active "projects"}} class="active" aria-current="page"{{end}}>Projects</a><a href="/logs"{{if eq .Active "logs"}} class="active" aria-current="page"{{end}}>Logs</a><a class="github-btn" href="https://github.com/bobmcallan/satelle" target="_blank" rel="noopener" aria-label="GitHub" title="GitHub">{{template "github-svg"}}</a></nav>{{with .User}}<span class="avatar" title="{{.Name}} · {{.Email}}" aria-label="Signed in as {{.Name}} ({{.Email}})">{{.Initial}}</span>{{end}}<button class="theme-toggle" id="theme-toggle" type="button" title="Toggle light/dark" aria-label="Toggle light/dark theme">☾</button></div></div></header>{{end}}

{{/* brandmark-svg: the satelle mark — a half-shaded sphere whose terminator sweeps
     the moon-phase cycle. Pure SMIL, no JS; fill/stroke use currentColor so it
     inherits the .brand-mark anchor colour (accent, --ink on hover, both themes).
     Reduced-motion users get the static ◐ via the embedded media rule (sty_8c00b58a). */}}
{{define "brandmark-svg"}}<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="20" height="20" role="img" aria-label="satelle">
    <style>
      #static { display: none; }
      @media (prefers-reduced-motion: reduce) {
        #anim { display: none; }
        #static { display: block; }
      }
    </style>
    <circle cx="50" cy="50" r="40" fill="none" stroke="currentColor" stroke-width="3"/>
    <path id="anim" fill="currentColor" d="M50 11 C-2 11 -2 89 50 89 C50 89 50 11 50 11 Z">
      <animate attributeName="d" dur="12s" repeatCount="indefinite" calcMode="linear"
        keyTimes="0;0.25;0.2501;0.5;0.75;1"
        values="M50 11 C-2 11 -2 89 50 89 C50 89 50 11 50 11 Z;
                M50 11 C-2 11 -2 89 50 89 C-2 89 -2 11 50 11 Z;
                M50 11 C102 11 102 89 50 89 C102 89 102 11 50 11 Z;
                M50 11 C50 11 50 89 50 89 C102 89 102 11 50 11 Z;
                M50 11 C-2 11 -2 89 50 89 C102 89 102 11 50 11 Z;
                M50 11 C-2 11 -2 89 50 89 C50 89 50 11 50 11 Z"/>
    </path>
    <path id="static" fill="currentColor" d="M50 11 A39 39 0 0 0 50 89 Z"/>
  </svg>{{end}}

{{/* github-svg: the octocat mark for the nav's outlined GitHub icon button
     (sty_2faa7dd4). fill=currentColor so it inherits the .github-btn ink colour. */}}
{{define "github-svg"}}<svg viewBox="0 0 16 16" width="16" height="16" role="img" aria-hidden="true" focusable="false" fill="currentColor"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.02-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.65 7.65 0 0 1 2-.27c.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z"/></svg>{{end}}

{{/* freshnessBare: the <time> only — LastIngest as a ticker-backed relative
     phrase: the last time satelled's mirror was confirmed, by a push or by
     the automatic sync pass (sto_128803b3). Landing Updated cells use this
     bare form (sty_a447695e): no
     "local:" prefix, no remote clock. The header composes freshness on top
     so both surfaces render one <time> from one place.

     The datetime attribute is what the ticker re-renders from, so the
     phrase stays current with no refetch and no focus. It is a plain absolute
     instant; the ticker never touches the title. */}}
{{define "freshnessBare"}}<time class="rel-time"{{if isotime .LastIngest}} datetime="{{isotime .LastIngest}}"{{end}} title="Last confirmed against the repository at {{ftime .LastIngest}}">{{reltime .LastIngest}}</time>{{end}}

{{/* freshness: project-header presentation. "local: updated" is a sibling of
     the <time>, never inside it — the app.js ticker rewrites only
     time.rel-time text (sty_226a661e, sty_8104248a). */}}
{{define "freshness"}}local: updated {{template "freshnessBare" .}}{{template "stalemark" .}}{{end}}

{{/* stalemark: shown only when no automatic sync pass (nor any push) has
     confirmed the partition for longer than mirror.StaleAfter (.Stale), so a
     mirror satelled is behind on never reads as current (sto_128803b3). The
     age is the <time> beside it, so the mark does not repeat it; its hover
     names since when and asks after the timer. Beside the <time>, never
     inside it, so the ticker leaves it alone. */}}
{{define "stalemark"}}{{if .Stale}} <span class="stale-flag" title="{{if isotime .LastIngest}}No confirmation since {{ftime .LastIngest}}{{else}}Never confirmed{{end}}; is satelle-sync.timer running?">stale</span>{{end}}{{end}}

{{/* syncstate: the last automatic ledger sync pass on the project header
     (sto_5c706973). "synced" stays outside time.rel-time so the ticker cannot
     strip it. Landing does not call this — it shows a Sync Failing tag
     instead. */}}
{{define "syncstate"}}{{if or .SyncLocal .SyncReason (isotime .SyncLastSuccess)}}remote: {{end}}{{if .SyncLocal}}<span class="sync-local" title="the ledger store is local: nothing replicates">local</span>{{else if .SyncReason}}<span class="sync-fail">sync failing</span>{{else if isotime .SyncLastSuccess}}<span class="sync-ok" title="Last sync with {{.SyncStore}} at {{ftime .SyncLastSuccess}}">synced <time class="rel-time" datetime="{{isotime .SyncLastSuccess}}">{{reltime .SyncLastSuccess}}</time></span>{{end}}{{end}}

{{define "syncfail"}}<div class="sync-fail-body"><span class="sync-fail-reason">{{.SyncReason}}</span>{{if isotime .SyncLastSuccess}} <span class="sync-fail-log">last synced <time class="rel-time" datetime="{{isotime .SyncLastSuccess}}">{{reltime .SyncLastSuccess}}</time></span>{{end}}</div>{{end}}

{{/* syncheld: stories the last push held until their code is on the remote
     (sty_c203fd62). In flight, not failing: muted, never inside syncfail. */}}
{{define "syncheld"}}<div class="sync-held" title="held until their code is on the remote — they push once it is">{{range .SyncHeld}}<div class="sync-held-item">held: <code>{{.ID}}</code> — {{.Reason}}</div>{{end}}</div>{{end}}

{{/* syncfailtag: landing-only status chip. No reason, no log path — those
     stay on the project page (sty_a447695e). */}}
{{define "syncfailtag"}}{{if and .SyncReason (not .SyncLocal)}}<span class="sync-tag-fail" title="ledger sync is failing — see the project page">Sync Failing</span>{{end}}{{end}}

{{/* footer: the three builds (sto_49b6e762): the satelle CLI installed beside
     satelled, the running satelled and the satelled installed on disk, each
     row marked when it is part of a disagreement, then one warning per
     disagreement naming its remedy (buildinfo.Drift, as satelle service status). */}}
{{define "footer"}}<footer class="site-footer">{{with .Builds}}<span class="footer-builds"><span class="footer-build{{if index .Mark "cli"}} mismatch{{end}}"><span class="footer-label">satelle</span> {{or .CLI "unknown"}}</span><span class="footer-build{{if index .Mark "running"}} mismatch{{end}}"><span class="footer-label">running</span> <span class="footer-version">{{.Running}}</span></span><span class="footer-build{{if index .Mark "installed"}} mismatch{{end}}"><span class="footer-label">installed</span> {{or .Installed "unknown"}}</span></span>{{range .Warnings}}<span class="footer-warning" role="status">⚠ {{.Text}} — run <code>{{.Remedy}}</code></span>{{end}}{{end}}</footer>{{end}}

{{/* favicon: satelle.dev ◐ monogram (animated terminator + reduced-motion static),
     one shared partial so every page <head> links the same icon — no per-page drift. */}}
{{define "favicon"}}<link rel="icon" type="image/svg+xml" href="/static/favicon.svg"><link rel="apple-touch-icon" href="/static/favicon.svg">{{end}}

{{define "page"}}<!doctype html>
<html lang="en"{{if .Theme}} data-theme="{{.Theme}}"{{end}}>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · project</title>
<script>(function(){try{if(!document.documentElement.getAttribute('data-theme')){var t=localStorage.getItem('satelle-theme');if(t==='dark')document.documentElement.setAttribute('data-theme','dark');}}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs">{{template "rootcrumb"}} <span class="sep">/</span> {{if gt (len .Projects) 1}}<details class="proj-switch"><summary class="cur">{{.ProjectName}} <span class="chev" aria-hidden="true">▾</span></summary><ul class="proj-menu">{{range .Projects}}<li><a href="/{{.Slug}}/" title="{{.Path}}"{{if .Current}} class="current" aria-current="page"{{end}}>{{.Name}}{{if .Ambiguous}} <span class="proj-slug">{{.Path}}</span>{{end}}</a></li>{{end}}</ul></details>{{else}}<span class="cur">{{.ProjectName}}</span>{{end}}</nav>
  <header class="app">
    <h1>{{.ProjectName}}</h1>
    <div class="meta">{{.RepoRoot}} · <a href="help">help →</a> · <a href="settings">settings →</a> · {{template "freshness" .}} · {{template "syncstate" .}}</div>
    {{if and .SyncReason (not .SyncLocal)}}{{template "syncfail" .}}{{end}}
    {{if and .SyncHeld (not .SyncLocal)}}{{template "syncheld" .}}{{end}}
  </header>

  <div class="tabs" role="tablist">
    {{/* Engagement badge is a SIBLING of the Stories tab <a>, not a child: when
         count is 1 the badge contains its own <a href="story/…"> and nested
         anchors are invalid HTML — browsers rewrite the DOM and the chip
         floats misaligned between Stories and Tasks (sty_01ba9482). */}}
    <span class="tab-cluster">
      <a class="tab" role="tab" data-panel="stories" href="#stories"><span class="tab-label" data-text="Stories">Stories</span> <span class="n">{{len .Stories}}</span>{{template "backlogBadge" .}}</a>
      {{template "engagementBadge" .}}
    </span>
    <a class="tab" role="tab" data-panel="activity" href="#activity"><span class="tab-label" data-text="Activity">Activity</span> <span class="n">{{len .Activity}}</span></a>
    <a class="tab" role="tab" data-panel="tasks" href="#tasks"><span class="tab-label" data-text="Tasks">Tasks</span> <span class="n">{{len .Tasks}}</span></a>
    <a class="tab" role="tab" data-panel="queues" href="#queues"><span class="tab-label" data-text="Queues">Queues</span> <span class="n">{{len .Queues}}</span></a>
    <a class="tab" role="tab" data-panel="workflow" href="#workflow"><span class="tab-label" data-text="Workflow">Workflow</span> <span class="n">{{len .Workflows}}</span></a>
    {{range .Panels}}{{template "panelTab" .}}{{end}}
  </div>

  <section class="panel" data-topic="stories" id="panel-stories">
    {{with .Hosted}}<p class="hosted-links">On the server: <a href="{{.Project}}">all stories</a></p>{{end}}
    <div class="filterbar">
      <div class="filter-input">
        <input type="text" placeholder="filter… e.g. status:all workflow:name tags:key:value order:updated" aria-label="filter stories">
        <span class="filter-count" aria-live="polite"></span>
      </div>
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>ID</th><th>Title</th><th>Status</th><th>Workflow</th><th>Assignees</th><th>Updated</th></tr></thead>
      <tbody data-rows>{{template "workitemRows" .Stories}}</tbody>
    </table>
  </section>

  <section class="panel" data-topic="activity" id="panel-activity">
    <div class="filterbar">
      <div class="filter-input">
        <input type="text" placeholder="filter… e.g. kind:story task:id order:updated" aria-label="filter activity">
        <span class="filter-count" aria-live="polite"></span>
      </div>
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>ID</th><th>Title · Kind</th><th>State</th><th>Working</th><th>Latest step</th><th>When</th></tr></thead>
      <tbody data-rows>{{template "activityRows" .Activity}}</tbody>
    </table>
  </section>

  <section class="panel" data-topic="tasks" id="panel-tasks">
    <div class="filterbar">
      <input type="text" placeholder="filter… e.g. status:all workflow:name order:title" aria-label="filter tasks">
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>ID</th><th>Title</th><th>Status</th><th>Workflow</th><th>Assignees</th><th>Updated</th></tr></thead>
      <tbody data-rows>{{template "workitemRows" .Tasks}}</tbody>
    </table>
  </section>

  <section class="panel" data-topic="queues" id="panel-queues">
    <div class="filterbar">
      <input type="text" placeholder="filter… e.g. status:all order:updated" aria-label="filter queues">
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>ID</th><th>Queue · Filter</th><th>Members</th><th>Trigger</th><th>Last fire</th><th>Updated</th></tr></thead>
      <tbody data-rows>{{template "queueRows" .Queues}}</tbody>
    </table>
  </section>

  <section class="panel" data-topic="workflow" id="panel-workflow">
    <div class="filterbar">
      <input type="text" placeholder="filter workflows…" aria-label="filter workflows">
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>Name</th><th>States</th><th>Source</th></tr></thead>
      <tbody data-rows>{{template "workflowRows" .Workflows}}</tbody>
    </table>
  </section>

  {{range .Panels}}
  <section class="panel" data-topic="{{.Topic}}" id="panel-{{.Topic}}">
    <div class="filterbar">
      <div class="filter-input">
        <input type="text" placeholder="filter… e.g. status:all workflow:name tags:key:value order:updated" aria-label="filter {{.Name}}">
        <span class="filter-count" aria-live="polite"></span>
      </div>
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>ID</th><th>Title</th><th>Status</th><th>Workflow</th><th>Assignees</th><th>Updated</th></tr></thead>
      <tbody data-rows>{{template "workitemRows" .Rows}}</tbody>
    </table>
  </section>
  {{end}}

  {{template "footer" .TopBar}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}

{{/* panelTab: a workflow panel's tab, the one renderer of its count (its
     items not in a terminal state) and its alert (tab-alert while that count
     is above 0). Rendered at page load and served at fragment/tab/<topic>;
     app.js copies the fragment onto the tab on each live refresh. */}}
{{define "panelTab"}}<a class="tab tab-wf{{if gt .Open 0}} tab-alert{{end}}" role="tab" data-panel="{{.Topic}}" data-wf-panel href="#{{.Topic}}"><span class="tab-label" data-text="{{.Name}}">{{.Name}}</span> <span class="n">{{.Open}}</span></a>{{end}}

{{/* backlogBadge: the Stories tab's 'N backlog' chip when BacklogCount > 0; emits
     nothing at 0. Rendered inside the tab anchor at page load and live-refreshed via
     GET fragment/backlog — JS swaps, inserts or removes it. */}}
{{define "backlogBadge"}}{{if gt .BacklogCount 0}}<span class="n-backlog" title="stories in the open backlog" data-backlog-count="{{.BacklogCount}}">{{.BacklogCount}} backlog</span>{{end}}{{end}}

{{/* engagementBadge: engaged-story count chip when EngagementCount > 0; emits nothing
     at 0 (sty_e4632f45). Live-refreshed via GET fragment/engagement — JS insert/remove
     handles 0↔n. Count = non-terminal stories holding a non-stale seat. */}}
{{define "engagementBadge"}}{{$n := .EngagementCount}}{{$ids := .EngagedStoryIDs}}{{if gt $n 0}}{{$title := printf "engaged: %s" (join $ids ", ")}}<span class="n-engaged has-engaged" data-engagement-count="{{$n}}" title="{{$title}}" aria-label="{{$n}} story engaged">{{if eq $n 1}}{{with index $ids 0}}<a class="n-engaged-link" href="story/{{.}}">1 engaged</a>{{end}}{{else}}{{$n}} engaged{{end}}</span>{{end}}{{end}}

{{define "assignees"}}{{if .}}{{range $i, $a := .}}{{if $i}}, {{end}}{{$a.User}}{{if $a.Working}} (working){{end}}{{end}}{{else}}—{{end}}{{end}}

{{define "workitemRows"}}{{range .}}<tr class="row" tabindex="0" role="button" aria-expanded="false" data-kind="{{.Kind}}"{{if eq (printf "%s" .Kind) "execution"}} data-task="{{.ParentID}}"{{end}} data-status="{{.Status}}"{{if eq .StateClass "terminal"}} data-terminal="1"{{end}} data-workflow="{{.Workflow}}" data-assignee="{{users .Assignees}}" data-category="{{.Category}}" data-tags="{{join .Tags ","}}" data-title="{{lower .Title}}" data-updated="{{.UpdatedAt.Format "2006-01-02T15:04:05"}}" data-created="{{.CreatedAt.Format "2006-01-02T15:04:05"}}" data-search="{{printf "%s %s %s %s" .Title .ID (users .Assignees) (join .Tags " ") | lower}}"{{if .Here}} data-here="1"{{end}} data-expand-url="fragment/{{.Kind}}/{{.ID}}">
  <td class="id"><span class="id-copy" role="button" tabindex="0" data-id="{{.ID}}" title="Copy id to clipboard">{{.ID}}</span></td>
  <td><div class="wi-title">{{.Title}}{{if .Here}} <span class="here-mark" title="engaged at this checkout">◆ here</span>{{end}}</div>{{$task := and (eq (printf "%s" .Kind) "execution") .ParentID}}{{if or .Category .Tags $task}}<div class="wi-tags">{{with $task}}{{tagchip (printf "task:%s" .)}}{{end}}{{if .Category}}{{tagchip (printf "category:%s" .Category)}}{{end}}{{range .Tags}}{{tagchip .}}{{end}}</div>{{end}}</td>
  <td>{{template "statebadge" .}}</td>
  <td class="meta">{{if .Workflow}}{{.Workflow}}{{else}}—{{end}}</td>
  <td class="meta">{{template "assignees" .Assignees}}</td>
  <td class="updated">{{ftime .UpdatedAt}}</td>
</tr>{{else}}<tr><td colspan="6" class="empty">none yet</td></tr>{{end}}{{end}}

{{/* activityRows: the items being worked now, one condensed line each: who
     or what works it (seats, else the state's performer) and its latest step.
     Same row attributes as workitemRows, so the filters and expand work. */}}
{{define "activityRows"}}{{range .}}<tr class="row" tabindex="0" role="button" aria-expanded="false" data-kind="{{.Kind}}"{{if eq (printf "%s" .Kind) "execution"}} data-task="{{.ParentID}}"{{end}} data-status="{{.Status}}" data-workflow="{{.Workflow}}" data-assignee="{{users .Assignees}}" data-category="{{.Category}}" data-tags="{{join .Tags ","}}" data-title="{{lower .Title}}" data-updated="{{.StepAt.Format "2006-01-02T15:04:05"}}" data-created="{{.CreatedAt.Format "2006-01-02T15:04:05"}}" data-search="{{printf "%s %s %s %s" .Title .ID (users .Assignees) (join .Tags " ") | lower}}" data-expand-url="fragment/{{.Kind}}/{{.ID}}">
  <td class="id"><span class="id-copy" role="button" tabindex="0" data-id="{{.ID}}" title="Copy id to clipboard">{{.ID}}</span></td>
  <td><div class="wi-title">{{.Title}}</div><div class="wi-tags"><span class="meta act-kind">{{.Kind}}</span>{{$task := and (eq (printf "%s" .Kind) "execution") .ParentID}}{{with $task}} {{tagchip (printf "task:%s" .)}}{{end}}</div></td>
  <td>{{template "statebadge" .}}</td>
  <td class="meta act-working">{{range $i, $w := .Working}}{{if $i}}<br>{{end}}{{$w}}{{else}}—{{end}}</td>
  <td class="meta act-step"{{with .StepTitle}} title="{{.}}"{{end}}>{{if .Step}}{{.Step}}{{else}}—{{end}}</td>
  <td class="updated">{{ftime .StepAt}}</td>
</tr>{{else}}<tr><td colspan="6" class="empty">no agent activity</td></tr>{{end}}{{end}}

{{/* queueRows: one row per queue — its filter, members now (added or by
     filter), trigger and last fire with its run's status. Same row attributes
     as workitemRows, so the filters and expand work. */}}
{{define "queueRows"}}{{range .}}<tr class="row" tabindex="0" role="button" aria-expanded="false" data-kind="{{.Kind}}" data-status="{{.Status}}"{{if eq .StateClass "terminal"}} data-terminal="1"{{end}} data-workflow="{{.Workflow}}" data-assignee="{{users .Assignees}}" data-category="{{.Category}}" data-tags="{{join .Tags ","}}" data-title="{{lower .Title}}" data-updated="{{.UpdatedAt.Format "2006-01-02T15:04:05"}}" data-created="{{.CreatedAt.Format "2006-01-02T15:04:05"}}" data-search="{{printf "%s %s %s" .Title .ID .Trigger | lower}}" data-expand-url="fragment/{{.Kind}}/{{.ID}}">
  <td class="id"><span class="id-copy" role="button" tabindex="0" data-id="{{.ID}}" title="Copy id to clipboard">{{.ID}}</span></td>
  <td><div class="wi-title">{{.Title}}</div><div class="wi-tags"><span class="meta q-filter">filter {{.Filter}}</span></div></td>
  <td class="meta q-members"><span class="n">{{len .Members}}</span>{{range .Members}}<br><span class="q-member" data-member="{{.ID}}">{{.ID}} <span class="badge">{{.Status}}</span>{{if .Explicit}} added{{end}}</span>{{end}}</td>
  <td class="meta q-trigger">{{.Trigger}}</td>
  <td class="meta q-fire">{{with .LastFire}}{{ftime .At}} · {{.Cause}}<br>run <span class="q-run" data-run="{{.Run}}">{{.Run}}</span> <span class="badge">{{.RunStatus}}</span> · {{.Inputs}} items{{else}}never{{end}}</td>
  <td class="updated">{{ftime .UpdatedAt}}</td>
</tr>{{else}}<tr><td colspan="6" class="empty">no queues</td></tr>{{end}}{{end}}

{{/* rootcrumb: the breadcrumb root, this machine's projects (the Projects
     page). One owner for its label. */}}
{{define "rootcrumb"}}<a href="/">projects</a>{{end}}

{{/* statebadge: an item's status, classed by its workflow (start, terminal,
     park or active), never by the state's name. */}}
{{define "statebadge"}}<span class="badge{{with .StateClass}} st-{{.}}{{end}}">{{.Status}}</span>{{end}}

{{define "docsRows"}}{{range .}}{{$k := .Kind}}<div class="kind-h">{{.Kind}}</div>{{if .Docs}}<div class="docgrid">{{range .Docs}}<a class="doc" href="doc/{{$k}}/{{.Name}}" data-search="{{printf "%s %s" .Name .Headline | lower}}">
  <div class="name">{{.Name}}</div>
  {{if .Headline}}<div class="head">{{.Headline}}</div>{{end}}
  {{if not .ModTime.IsZero}}<div class="updated">updated {{ftime .ModTime}}</div>{{end}}
</a>{{end}}</div>{{else}}<div class="empty">none</div>{{end}}{{else}}<div class="empty">Repo documents are removed; workflows are on the Workflow tab.</div>{{end}}{{end}}

{{define "workflowRows"}}{{range .}}<tr class="row" tabindex="0" role="button" aria-expanded="false" data-search="{{printf "%s %s" .Name .Source | lower}}" data-expand-url="fragment/workflow/{{.Name}}">
  <td><div class="wi-title">{{.Name}}</div></td>
  <td>{{.States}}</td>
  <td class="meta mono">{{.Source}}</td>
</tr>{{else}}<tr><td colspan="3" class="empty">none pushed — run <code>satelle project add</code></td></tr>{{end}}{{end}}

{{define "workflowDetail"}}<div class="expbody">
  <a class="detail-link" href="doc/{{.Kind}}/{{.Name}}">Open workflow →</a>
  {{with .Workflow}}{{template "route" .}}{{end}}
</div>{{end}}

{{/* route: a workflow drawn as pushed — its states in route order (current
     highlighted, start, park and terminal marked, visited filled) and its
     explicit transitions with their gate groups. */}}
{{define "route"}}{{if .Missing}}<div class="empty">{{if .Workflow}}Workflow <code>{{.Workflow}}</code> was not pushed; run <code>satelle project add</code> to push it.{{else}}This item names no workflow.{{end}}</div>{{else}}
  <ol class="route-line" aria-label="route of workflow {{.Workflow}}">{{range .States}}<li class="route-state{{if .Current}} current{{end}}{{if .Visited}} visited{{end}}{{if .Terminal}} terminal{{end}}{{if .Park}} park{{end}}{{if .Start}} start{{end}}"{{if .Current}} aria-current="step"{{end}}>
    <span class="wf-node">{{.Name}}</span>{{if .Start}}<span class="route-flag">start</span>{{end}}{{if .Park}}<span class="route-flag">park{{if .Resumes}} · resumes{{end}}</span>{{end}}{{if .Terminal}}<span class="route-flag">terminal</span>{{end}}{{if .PerformerAgent}}<span class="route-perf" title="performer{{if .PerformerSkill}} under skill {{.PerformerSkill}}{{end}}">{{.PerformerAgent}}{{if .PerformerSkill}} · {{.PerformerSkill}}{{end}}</span>{{end}}
  </li>{{end}}</ol>
  <table class="panel-table route-transitions">
    <thead><tr><th>From</th><th>To</th><th>Gates</th><th>Taken</th></tr></thead>
    <tbody>{{range .Transitions}}<tr class="{{if .Next}}next{{end}}{{if .Taken}} taken{{end}}">
      <td>{{join .From ", "}}</td>
      <td>{{.To}}</td>
      <td>{{range .Groups}}<span class="gate-group mode-{{.Mode}}" title="{{.Mode}}">{{range .Gates}}<span class="wf-gate">{{.}}</span>{{end}}</span>{{else}}<span class="wf-gate ungated">ungated</span>{{end}}</td>
      <td>{{if .Taken}}{{.Taken}}×{{else}}—{{end}}</td>
    </tr>{{end}}</tbody>
  </table>{{end}}{{end}}

{{define "itemDetail"}}<div class="expbody">
  {{if not .Standalone}}<a class="detail-link open-story" href="{{.Item.Kind}}/{{.Item.ID}}">Open {{.Item.Kind}} →</a>{{end}}
  <dl>
    <dt>Status</dt><dd>{{template "statebadge" (dict "Status" .Item.Status "StateClass" .StateClass)}}</dd>
    <dt>Workflow</dt><dd>{{if .Item.Workflow}}{{.Item.Workflow}}{{else}}—{{end}}</dd>
    {{if .Item.Category}}<dt>Category</dt><dd>{{.Item.Category}}</dd>{{end}}
    {{if .Assignees}}<dt>Assignees</dt><dd>{{template "assignees" .Assignees}}</dd>{{end}}
    {{if .Item.ParentID}}<dt>Parent</dt><dd>{{if .ParentKind}}<a href="{{.ParentKind}}/{{.Item.ParentID}}">{{.Item.ParentID}}</a>{{else}}{{.Item.ParentID}}{{end}}</dd>{{end}}
    {{if .Children}}<dt>Children</dt><dd><ul class="child-list">{{range .Children}}<li><a href="{{.Kind}}/{{.ID}}">{{.ID}}</a> {{.Title}} <span class="badge">{{.Status}}</span></li>{{end}}</ul></dd>{{end}}
    {{if .Item.Tags}}<dt>Tags</dt><dd class="wi-tags">{{range .Item.Tags}}{{tagchip .}}{{end}}</dd>{{end}}
    <dt>Updated</dt><dd>{{ftime .Item.UpdatedAt}}</dd>
  </dl>
  {{if .Item.Body}}<h4>Description</h4><pre class="prose">{{.Item.Body}}</pre>{{end}}
  {{if .Item.Criteria}}<h4>Criteria</h4><ul class="criteria">{{range .Item.Criteria}}<li>{{.}}</li>{{end}}</ul>{{else if .Item.AcceptanceCriteria}}<h4>Criteria</h4><pre class="prose">{{.Item.AcceptanceCriteria}}</pre>{{end}}
  <h4>Route</h4>
  <section class="route" data-workflow="{{.Route.Workflow}}">{{template "route" .Route}}</section>
  <h4>Verdicts</h4>
  {{if .Presentations}}<ol class="verdicts">{{range .Presentations}}<li class="presentation outcome-{{.Outcome}}">
    <div class="pres-head"><span class="wf-node sm">{{.From}}</span> → <span class="wf-node sm">{{.To}}</span> <span class="pres-outcome">{{.Outcome}}</span> <span class="meta">{{ftime .At}}</span></div>
    {{if .Verdicts}}<table class="panel-table verdict-table">
      <thead><tr><th>Gate</th><th>Binding</th><th>Result</th><th>Tokens</th><th>Duration</th><th>Notes</th></tr></thead>
      <tbody>{{range .Verdicts}}<tr class="verdict v-{{.Result}}">
        <td>{{.Gate}}</td>
        <td class="meta">{{if .Binding}}{{.Binding}}{{else}}—{{end}}</td>
        <td><span class="verdict-result">{{.Result}}</span></td>
        <td class="meta">{{if or .TokensIn .TokensOut}}{{.TokensIn}} in · {{.TokensOut}} out{{else}}—{{end}}</td>
        <td class="meta">{{if .Duration}}{{.Duration}}{{else}}—{{end}}</td>
        <td>{{if .Notes}}<pre class="verdict-notes">{{.Notes}}</pre>{{end}}</td>
      </tr>{{end}}</tbody>
    </table>{{else}}<div class="meta">no gate on this transition</div>{{end}}
  </li>{{end}}</ol>{{else}}<div class="empty">No transition presented yet.</div>{{end}}
  <h4>Attachments</h4>
  {{if .Attachments}}<ul class="doc-list">{{range .Attachments}}<li><details class="doc-item" id="{{.Anchor}}"><summary>{{.Name}} <span class="doc-item-type">{{.MediaType}}</span> <span class="meta">{{.Size}} bytes{{if .Short}} · sha256 <code title="{{.SHA256}}">{{.Short}}</code>{{end}}{{if not .At.IsZero}} · {{ftime .At}}{{end}}{{if gt .Versions 1}} · {{.Versions}} versions{{end}}</span></summary>{{if .HTML}}<article class="doc-article">{{.HTML}}</article>{{else if .Text}}<pre class="prose">{{.Text}}</pre>{{else if .BodyOmitted}}<div class="meta">body not pushed (over the push bound or not text)</div>{{else}}<div class="meta">no body</div>{{end}}</details></li>{{end}}</ul>{{else}}<div class="empty">No attachments.</div>{{end}}
  <h4>Timeline</h4>
  {{if .Events}}<ol class="timeline">{{range .Events}}<li{{with evdot .}} class="{{.}}"{{end}}>
    <div class="ev-kind">{{.Kind}}</div>
    <div class="ev-meta">{{ftime .At}}{{if .Actor}} · {{.Actor}}{{end}}</div>
    {{if .DocName}}<div class="ev-doc">{{if .DocHref}}<a href="{{.DocHref}}">{{.DocName}}</a>{{else}}{{.DocName}}{{end}}</div>{{end}}
    {{if .Body}}<div class="ev-body">{{.Body}}</div>{{end}}
    {{if .Chips}}<div class="ev-chips">{{range .Chips}}<span class="chip chip-{{.Type}}">{{.Label}}</span>{{end}}</div>{{end}}
  </li>{{end}}</ol>{{else}}<div class="empty">No ledger events yet.</div>{{end}}
</div>{{end}}

{{define "help"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · help</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs"><a href="{{basehref}}">project</a> <span class="sep">/</span> <span class="cur">help</span></nav>
  <header class="app">
    <h1>satelle<span class="dot">.</span> help</h1>
    <div class="meta">process guides · the same content as <code>satelle help</code></div>
  </header>
  {{range .Topics}}<section class="help-topic" id="{{.Name}}">
    <h2 class="kind-h">{{.Title}} <span class="meta">{{.Name}}</span></h2>
    <article class="doc-article help-doc">{{.HTML}}</article>
  </section>{{else}}<div class="empty">no help topics</div>{{end}}
  {{template "footer" .TopBar}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}

{{define "settings"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · settings</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs"><a href="{{basehref}}">project</a> <span class="sep">/</span> <span class="cur">settings</span></nav>
  <header class="app">
    <h1>satelle<span class="dot">.</span> settings</h1>
    <div class="meta">{{.RepoRoot}} · read-only view of <code>.satelle/satelle.toml</code></div>
  </header>
  <div class="settings-note settings-readonly-note">Repo settings are read-only here — edit <code>.satelle/satelle.toml</code> directly (and commit it under the workflow) to change them. Machine-wide hosted server lives on <a href="/settings/global">global settings</a>.</div>
  <div class="settings">
    {{range .Rows}}{{if .SectHead}}<h2 class="kind-h settings-sect">{{.SectHead}}</h2>{{end}}<div class="setting-row">
      <div class="setting-label"><span class="setting-key">{{.FieldID}}</span><span class="setting-label-name">{{.Label}}</span>{{if .Help}}<span class="setting-help">{{.Help}}</span>{{end}}</div>
      <div class="setting-field"><div class="setting-value{{if not .Value}} setting-value-unset{{end}}">{{if .Value}}{{.Value}}{{else}}—{{end}}</div></div>
    </div>{{end}}
  </div>

  <h2 class="kind-h settings-sect">Display preferences</h2>
  <div class="settings-note">These are per-viewer display preferences stored in your browser (like the theme), not repo config — they change nothing on disk.</div>
  <div class="tlfields" role="group" aria-label="Timeline fields">
    <div class="tlfields-label">Timeline fields — which agent-action chips the story timeline shows:</div>
    <label><input type="checkbox" data-tlfield="walltime" checked> Wall-time</label>
    <label><input type="checkbox" data-tlfield="tokens" checked> Tokens</label>
    <label><input type="checkbox" data-tlfield="model" checked> Model/agent</label>
    <label><input type="checkbox" data-tlfield="outcome" checked> Outcome</label>
  </div>
  {{template "footer" .TopBar}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}

{{define "globalSettings"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · global settings</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs">{{template "rootcrumb"}} <span class="sep">/</span> <span class="cur">global settings</span></nav>
  <header class="app">
    <h1>global settings</h1>
    <div class="meta">machine-wide · <code>~/.satelle/config.toml</code> · follows you across every project</div>
  </header>
  {{if .Saved}}<div class="settings-saved" role="status">Saved.</div>{{end}}
  <form id="gsettings-form" class="settings" method="post" action="settings/global">
    <div class="setting-row">
      <div class="setting-label"><label for="g-server">Hosted server</label><span class="setting-help">The satelle-server you sign in to. Configuring it needs no login — sign in afterwards from the topbar. Leave blank to remove.</span></div>
      <div class="setting-field"><input type="text" id="g-server" name="server" value="{{.Server}}" spellcheck="false" autocomplete="off" placeholder="https://satelle.dev"></div>
    </div>
    <div class="setting-row">
      <div class="setting-label"><label>Theme</label><span class="setting-help">Light/dark, shared across every repo.</span></div>
      <div class="setting-field"><label class="gs-radio"><input type="radio" name="theme" value="light"{{if eq .Theme "light"}} checked{{end}}> light</label> <label class="gs-radio"><input type="radio" name="theme" value="dark"{{if eq .Theme "dark"}} checked{{end}}> dark</label></div>
    </div>
    <div class="settings-actions"><button type="submit" class="settings-save">Save</button><span class="settings-note">Writes are accepted only from this machine (loopback).</span></div>
    <div class="settings-error" id="gsettings-error" role="alert" hidden></div>
  </form>
  {{template "footer" .TopBar}}
</div>
<script src="/static/app.js"></script>
<script>
(function(){var f=document.getElementById('gsettings-form');if(!f)return;
f.addEventListener('submit',function(e){e.preventDefault();var err=document.getElementById('gsettings-error');err.hidden=true;
fetch('settings/global',{method:'POST',headers:{'X-Satelle-Settings':'1'},body:new FormData(f)})
.then(function(r){if(r.ok){window.location='settings/global?saved=1';}else{return r.text().then(function(t){err.textContent=t||('Error '+r.status);err.hidden=false;});}})
.catch(function(){err.textContent='Network error';err.hidden=false;});});})();
</script>
</body>
</html>{{end}}

{{define "docPage"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · {{.Name}}</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs"><a href="{{basehref}}">project</a> <span class="sep">/</span> <a href="{{basehref}}#workflow">{{.Kind}}</a> <span class="sep">/</span> <span class="cur">{{.Name}}</span></nav>
  <header class="app">
    <div class="kind-h">{{.Kind}}</div>
    <h1>{{.Name}}</h1>
    {{if .Headline}}<div class="meta">{{.Headline}}</div>{{end}}
    {{if .Source}}<div class="meta mono">source: {{.Source}}</div>{{end}}
  </header>
  {{with .Workflow}}<section class="route">{{template "route" .}}</section>{{end}}
  <article class="doc-article">{{.HTML}}</article>
  {{template "footer" .TopBar}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}

{{define "detailPage"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · {{.Item.ID}}</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs"><a class="back-link" href="{{basehref}}?expand={{.Item.ID}}#{{if .BackTab}}{{.BackTab}}{{else}}{{tabof .Item.Kind}}{{end}}" aria-label="Back to project">←</a> <a href="{{basehref}}">project</a> <span class="sep">/</span> <a href="{{basehref}}#{{if .BackTab}}{{.BackTab}}{{else}}{{tabof .Item.Kind}}{{end}}">{{.Item.Kind}}</a> <span class="sep">/</span> <span class="cur">{{.Item.ID}}</span></nav>
  <header class="app">
    <div class="kind-h">{{.Item.Kind}}</div>
    <h1>{{.Item.Title}}</h1>
    <div class="meta">{{.Item.ID}}{{if .Item.Workflow}} · {{.Item.Workflow}}{{end}} · {{template "statebadge" (dict "Status" .Item.Status "StateClass" .StateClass)}}</div>
  </header>
  <div id="detail-live" data-kind="{{.Item.Kind}}" data-id="{{.Item.ID}}">{{template "itemDetail" .}}</div>
  {{template "footer" .TopBar}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}

{{define "logsLive"}}{{if .Rows}}{{range .Rows}}<tr class="log-row"{{if .Structured}} data-level="{{.Level}}"{{end}}>
      <td class="log-time">{{if .Structured}}{{ftime .Time}}{{else}}—{{end}}</td>
      <td class="log-level">{{if .Structured}}<span class="lvl-{{.Level}}">{{.Level}}</span>{{else}}—{{end}}</td>
      <td class="log-text mono">{{.Text}}</td>
    </tr>{{end}}{{else}}<tr class="log-empty"><td colspan="3"><div class="empty">No log lines yet.</div></td></tr>{{end}}{{end}}

{{define "logs"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · logs</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body data-page="logs">
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs">{{template "rootcrumb"}} <span class="sep">/</span> <span class="cur">logs</span></nav>
  <header class="app">
    <h1>logs</h1>
    <div class="meta">satelled · <code class="logs-path">{{.Path}}</code></div>
  </header>
  <nav class="logs-filter" aria-label="filter by level">
    <a href="/logs"{{if eq .Filter ""}} class="active" aria-current="page"{{end}}>all</a>
    <a href="/logs?level=info"{{if eq .Filter "info"}} class="active" aria-current="page"{{end}}>INFO</a>
    <a href="/logs?level=warn"{{if eq .Filter "warn"}} class="active" aria-current="page"{{end}}>WARN</a>
    <a href="/logs?level=error"{{if eq .Filter "error"}} class="active" aria-current="page"{{end}}>ERROR</a>
  </nav>
  <table class="panel-table logs-table">
    <thead><tr><th>Time</th><th>Level</th><th>Message</th></tr></thead>
    <tbody data-rows id="logs-live">{{template "logsLive" .}}</tbody>
  </table>
  {{template "footer" .TopBar}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}
`
