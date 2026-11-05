package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/buildinfo"
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
	// product / version back the one shared site footer (see the "footer"
	// template) so it needs no per-page data: they are baked into the binary
	// (per-artifact name via buildinfo.Name). The footer names no user; the
	// satelle user is shown once, in the top bar.
	"product": func() string { return buildinfo.Resolve().Name },
	"version": func() string { return buildinfo.Resolve().Version },
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
	// staleafter names mirror.StaleAfter for the stale marker's hover text.
	"staleafter": func() string { return fmt.Sprintf("%d min", int(mirror.StaleAfter.Minutes())) },
	"lower":      strings.ToLower,
	"join": func(ss []string, sep string) string {
		return strings.Join(ss, sep)
	},
	// evdot maps a ledger event kind to the timeline dot's outcome class, so the
	// dot speaks the same colour language as the process/progress lights: a
	// review_accept is a pass (green) dot, a review_reject a fail (red) dot. Every
	// neutral process event (story_created, status_transition, estimate/step rows,
	// …) returns "" and keeps the default accent dot. Outcome is derived here,
	// server-side, from the event kind alone. (sty_f19d2ec4)
	// evdot maps a timeline event to the dot's outcome class. The kernel's
	// pages (B4) colour by the gate result in the payload; the kind alone
	// names no outcome.
	"evdot": func(kind any) string { return "" },
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
		}
		return "stories"
	},
}

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
     Install/Docs open satelle.dev in a new tab, Projects is the local workspace
     landing, Logs is the machine-wide server.log (sty_df4a0785); active via
     .Active — then a GitHub OUTLINED ICON button (new tab, not a text link);
     the account control; and the theme toggle LAST. There are no Home/Help top-nav
     items — the mark IS the home affordance and Help stays on the meta/breadcrumb
     line. The mark folds in two signals: the uptime snapshot rides in its title
     tooltip, and the live /events (SSE) connection state is its colour — accent when
     connected, muted --fail-soft on the ◐ only (.sse-down, added by app.js) when the
     stream drops. The theme toggle glyph is ☾/☀ (app.js), never ◐. Mobile-collapsible
     nav is out of scope — the row stays inline. */}}
{{define "topbar"}}<header class="topbar"><div class="topbar-inner"><a class="brand-mark" href="https://satelle.dev/" target="_blank" rel="noopener" title="satelle — home{{if .Uptime}} · {{.Uptime}} at page load · mark colour = live-update connection{{end}}" aria-label="satelle home (opens in a new tab)">{{template "brandmark-svg"}}<span class="brand-word">satelle</span></a><div class="topbar-controls"><nav class="topnav"><a href="https://satelle.dev/install" target="_blank" rel="noopener">Install</a><a href="https://satelle.dev/docs" target="_blank" rel="noopener">Docs</a><a href="/"{{if eq .Active "projects"}} class="active" aria-current="page"{{end}}>Projects</a><a href="/logs"{{if eq .Active "logs"}} class="active" aria-current="page"{{end}}>Logs</a><a class="github-btn" href="https://github.com/bobmcallan/satelle" target="_blank" rel="noopener" aria-label="GitHub" title="GitHub">{{template "github-svg"}}</a></nav>{{if .MirrorRO}}{{if .IdentityEmail}}<span class="signin" title="Read-only local UI — project data pushed by the CLI (not live-edited here)" aria-label="Operator identity {{.IdentityEmail}}; read-only local UI, project data pushed by the CLI">{{.IdentityEmail}}</span>{{end}}{{else}}{{template "account" .User}}{{end}}<button class="theme-toggle" id="theme-toggle" type="button" title="Toggle light/dark" aria-label="Toggle light/dark theme">☾</button></div></div></header>{{end}}

{{/* account: the hosted-server sign-in control (sty_9ae98484, sty_2faa7dd4).
     Signed out → a "Sign in" link (relative href, so the <base> resolves the /slug/
     prefix on a supervised child). Signed in → an avatar (initial, email tooltip)
     that opens a dropdown with: the identity; the LOCAL Project settings + GLOBAL
     settings links; a "Remove server" quick action that clears the global [hosted]
     server (a POST to settings/global with an empty server — a fetch in app.js
     attaches the CSRF header a bare form can't set); and a Sign out form (POST). The
     dropdown reuses the <details> idiom the breadcrumb switcher proves. */}}
{{define "account"}}{{if .}}<details class="account"><summary class="avatar" title="{{.Email}}" aria-label="Account menu for {{.Name}}">{{.Initial}}</summary><div class="account-menu"><div class="acct-id"><strong>{{.Name}}</strong><span class="acct-email">{{.Email}}</span></div><div class="acct-div"></div><a class="acct-link" href="settings" role="menuitem">Project settings</a><a class="acct-link" href="settings/global" role="menuitem">Global settings</a><form class="acct-remove-form" method="post" action="settings/global"><input type="hidden" name="server" value=""><button type="submit" class="acct-remove-server" role="menuitem" title="Clear the global hosted server (~/.satelle/config.toml) and sign out everywhere">Remove server</button></form><div class="acct-div"></div><form method="post" action="oauth/logout"><button type="submit" class="acct-signout">Sign out</button></form></div></details>{{else}}<a class="signin" href="oauth/login" title="Sign in to the hosted satelle-server">Sign in</a>{{end}}{{end}}

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
     phrase. Landing Updated cells use this bare form (sty_a447695e): no
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

{{/* stalemark: shown only when the partition has had no confirmed snapshot
     for longer than mirror.StaleAfter (.Stale), so a frame the serving tier
     could not repair never reads as current. Beside the <time>, never inside
     it, so the ticker leaves it alone. */}}
{{define "stalemark"}}{{if .Stale}} <span class="stale-flag" title="No confirmed snapshot for over {{staleafter}}">stale · {{reltime .LastIngest}}</span>{{end}}{{end}}

{{/* syncstate: hosted workstate plane on the project header (sty_8104248a).
     "pushed" stays outside time.rel-time so the ticker cannot strip it.
     Landing does not call this — it shows a Push Failing tag instead. */}}
{{define "syncstate"}}{{if or .SyncLocal .SyncReason (isotime .SyncLastSuccess)}}remote: {{end}}{{if .SyncLocal}}<span class="sync-local" title="work-state areas are local">local</span>{{else if .SyncReason}}<span class="sync-fail">push failing</span>{{else if isotime .SyncLastSuccess}}<span class="sync-ok" title="Last successful hosted push at {{ftime .SyncLastSuccess}}">pushed <time class="rel-time" datetime="{{isotime .SyncLastSuccess}}">{{reltime .SyncLastSuccess}}</time></span>{{end}}{{end}}

{{define "syncfail"}}<div class="sync-fail-body"><span class="sync-fail-reason">{{.SyncReason}}</span>{{if .SyncLogPath}} <span class="sync-fail-log">logged to <code>{{.SyncLogPath}}</code></span>{{end}}</div>{{end}}

{{/* syncheld: stories the last push held until their code is on the remote
     (sty_c203fd62). In flight, not failing: muted, never inside syncfail. */}}
{{define "syncheld"}}<div class="sync-held" title="held until their code is on the remote — they push once it is">{{range .SyncHeld}}<div class="sync-held-item">held: <code>{{.ID}}</code> — {{.Reason}}</div>{{end}}</div>{{end}}

{{/* syncfailtag: landing-only status chip. No reason, no log path — those
     stay on the project page (sty_a447695e). */}}
{{define "syncfailtag"}}{{if and .SyncReason (not .SyncLocal)}}<span class="sync-tag-fail" title="hosted push is failing — see the project page">Push Failing</span>{{end}}{{end}}

{{define "footer"}}<footer class="site-footer"><span class="footer-version">{{product}} {{version}}</span></footer>{{end}}

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
  <nav class="crumbs"><a href="/">workspace</a> <span class="sep">/</span> {{if gt (len .Projects) 1}}<details class="proj-switch"><summary class="cur">{{.ProjectName}} <span class="chev" aria-hidden="true">▾</span></summary><ul class="proj-menu">{{range .Projects}}<li><a href="/{{.Slug}}/" title="{{.Path}}"{{if .Current}} class="current" aria-current="page"{{end}}>{{.Name}}{{if .Ambiguous}} <span class="proj-slug">{{.Path}}</span>{{end}}</a></li>{{end}}</ul></details>{{else}}<span class="cur">{{.ProjectName}}</span>{{end}}</nav>
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
      <a class="tab" role="tab" data-panel="stories" href="#stories"><span class="tab-label" data-text="Stories">Stories</span> <span class="n">{{len .Stories}}</span>{{if .BacklogCount}} <span class="n-backlog" title="stories in the open backlog">{{.BacklogCount}} backlog</span>{{end}}</a>
      {{template "engagementBadge" .}}
    </span>
    <a class="tab" role="tab" data-panel="activity" href="#activity"><span class="tab-label" data-text="Activity">Activity</span> <span class="n">{{len .Activity}}</span></a>
    <a class="tab" role="tab" data-panel="tasks" href="#tasks"><span class="tab-label" data-text="Tasks">Tasks</span> <span class="n">{{len .Tasks}}</span></a>
    <a class="tab" role="tab" data-panel="workflow" href="#workflow"><span class="tab-label" data-text="Workflow">Workflow</span> <span class="n">{{len .Workflows}}</span></a>
    <a class="tab" role="tab" data-panel="docs" href="#docs"><span class="tab-label" data-text="Documents">Documents</span> <span class="n">{{.DocCount}}</span></a>
  </div>

  <section class="panel" data-topic="stories" id="panel-stories">
    {{with .Hosted}}<p class="hosted-links">On the server: <a href="{{.Project}}">all stories</a></p>{{end}}
    <div class="filterbar">
      <div class="filter-input">
        <input type="text" placeholder="filter… e.g. status:open priority:high tags:epic:foo order:updated" aria-label="filter stories">
        <span class="filter-count" aria-live="polite"></span>
      </div>
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>ID</th><th>Title</th><th>Status</th><th>Progress</th><th>Priority</th><th>Updated</th></tr></thead>
      <tbody data-rows>{{template "workitemRows" .Stories}}</tbody>
    </table>
  </section>

  <section class="panel" data-topic="activity" id="panel-activity">
    <div class="filterbar">
      <div class="filter-input">
        <input type="text" placeholder="filter… e.g. kind:execution status:in_progress priority:high tags:epic:foo order:updated" aria-label="filter activity">
        <span class="filter-count" aria-live="polite"></span>
      </div>
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>ID</th><th>Title</th><th>Status</th><th>Progress</th><th>Priority</th><th>Updated</th></tr></thead>
      <tbody data-rows>{{template "workitemRows" .Activity}}</tbody>
    </table>
  </section>

  <section class="panel" data-topic="tasks" id="panel-tasks">
    <div class="filterbar">
      <input type="text" placeholder="filter… e.g. status:open priority:high order:title" aria-label="filter tasks">
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>ID</th><th>Title</th><th>Status</th><th>Progress</th><th>Priority</th><th>Updated</th></tr></thead>
      <tbody data-rows>{{template "workitemRows" .Tasks}}</tbody>
    </table>
  </section>

  <section class="panel" data-topic="workflow" id="panel-workflow">
    <div class="filterbar">
      <input type="text" placeholder="filter workflows…" aria-label="filter workflows">
      <div class="chips"></div>
    </div>
    <table class="panel-table">
      <thead><tr><th>Name</th><th>Summary</th><th>Updated</th></tr></thead>
      <tbody data-rows>{{template "workflowRows" .Workflows}}</tbody>
    </table>
  </section>

  <section class="panel" data-topic="docs" id="panel-docs">
    <div class="filterbar">
      <input type="text" placeholder="filter authored docs…" aria-label="filter documents">
      <div class="chips"></div>
    </div>
    <div data-rows>{{template "docsRows" .DocKinds}}</div>
  </section>

  {{template "footer"}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}

{{/* engagementBadge: story-seat count chip when EngagementCount > 0; emits nothing
     at 0 (sty_e4632f45). Live-refreshed via GET fragment/engagement — JS insert/remove
     handles 0↔n. Count = non-stale story_seat rows. */}}
{{define "engagementBadge"}}{{$n := .EngagementCount}}{{$ids := .EngagedStoryIDs}}{{if gt $n 0}}{{$title := printf "engaged: %s" (join $ids ", ")}}<span class="n-engaged has-engaged" data-engagement-count="{{$n}}" title="{{$title}}" aria-label="{{$n}} story engaged">{{if eq $n 1}}{{with index $ids 0}}<a class="n-engaged-link" href="story/{{.}}">1 engaged</a>{{end}}{{else}}{{$n}} engaged{{end}}</span>{{end}}{{end}}

{{/* dispatchIndicator: the running indicator of one in-flight dispatch (nil → nothing).
     One rendering for a story row and an epic's member row (sty_a42f4add). */}}
{{define "dispatchIndicator"}}{{with .}}<div class="dispatch-indicator{{if .Warn}} dispatch-warn{{end}}" title="{{if .LastEvent}}last activity: {{.LastEvent}}{{end}}">{{.Agent}}{{if .Model}} ({{.Model}}){{end}} running · {{reltime .StartedAt}}{{if not .LastEventAt.IsZero}} · last activity {{reltime .LastEventAt}}{{if .LastEvent}}: {{.LastEvent}}{{end}}{{end}}</div>{{end}}{{end}}


{{define "workitemRows"}}{{range .}}<tr class="row" tabindex="0" role="button" aria-expanded="false" data-kind="{{.Kind}}"{{if eq (printf "%s" .Kind) "execution"}} data-task="{{.ParentID}}"{{end}} data-status="{{.Status}}" data-priority="{{.Priority}}" data-category="{{.Category}}" data-tags="{{join .Tags ","}}" data-title="{{lower .Title}}" data-updated="{{.UpdatedAt.Format "2006-01-02T15:04:05"}}" data-created="{{.CreatedAt.Format "2006-01-02T15:04:05"}}" data-search="{{printf "%s %s %s" .Title .ID (join .Tags " ") | lower}}"{{if .Here}} data-here="1"{{end}} data-expand-url="fragment/{{.Kind}}/{{.ID}}">
  <td class="id"><span class="id-copy" role="button" tabindex="0" data-id="{{.ID}}" title="Copy id to clipboard">{{.ID}}</span></td>
  <td><div class="wi-title">{{.Title}}{{if .Here}} <span class="here-mark" title="engaged at this checkout">◆ here</span>{{end}}</div>{{$task := and (eq (printf "%s" .Kind) "execution") .ParentID}}{{if or .Category .Tags $task}}<div class="wi-tags">{{with $task}}{{tagchip (printf "task:%s" .)}}{{end}}{{if .Category}}{{tagchip (printf "category:%s" .Category)}}{{end}}{{range .Tags}}{{tagchip .}}{{end}}</div>{{end}}{{template "dispatchIndicator" .Dispatch}}</td>
  <td><span class="badge s-{{.Status}}">{{.Status}}</span></td>
  <td class="col-reviews">{{range .Stages}}<span class="stage-chip stage-{{.State}}" title="{{.Title}}">{{.Name}}{{with .Gates}}{{if .Gates}} <b class="gates tone-{{.Tone}}">{{.Gates}}</b>{{if .Live}}<b class="run live">/{{.Running}}</b>{{end}}{{if .Rejected}} <b class="rej">{{.Rejected}}</b>{{end}}{{end}}{{end}}{{if .Parked}} <b class="blk">{{.Parked}}</b>{{end}}</span>{{end}}</td>
  <td>{{if .Priority}}{{.Priority}}{{else}}—{{end}}</td>
  <td class="updated">{{ftime .UpdatedAt}}</td>
</tr>{{else}}<tr><td colspan="6" class="empty">none yet</td></tr>{{end}}{{end}}

{{define "docsRows"}}{{range .}}{{$k := .Kind}}<div class="kind-h">{{.Kind}}</div>{{if .Docs}}<div class="docgrid">{{range .Docs}}<a class="doc" href="doc/{{$k}}/{{.Name}}" data-search="{{printf "%s %s %s" .Name .Headline .Provenance | lower}}">
  <div class="name">{{.Name}}</div>
  {{if .Provenance}}<div class="wi-tags">{{tagchip (printf "provenance:%s" .Provenance)}}</div>{{end}}
  {{if .Headline}}<div class="head">{{.Headline}}</div>{{end}}
  {{if not .ModTime.IsZero}}<div class="updated">updated {{ftime .ModTime}}</div>{{end}}
</a>{{end}}</div>{{else}}<div class="empty">none indexed — run <code>satelle reindex</code></div>{{end}}{{end}}{{end}}

{{define "workflowRows"}}{{range .}}<tr class="row" tabindex="0" role="button" aria-expanded="false" data-search="{{printf "%s %s %s %s %s" .Name .Headline .Scope .Provenance (join .AppliesTo " ") | lower}}" data-expand-url="fragment/workflow/{{.ExpandName}}">
  <td><div class="wi-title">{{.Name}}</div><div class="wi-tags">{{if .Provenance}}{{tagchip (printf "provenance:%s" .Provenance)}}{{end}}{{if .Scope}}{{tagchip (printf "scope:%s" .Scope)}}{{end}}{{range .AppliesTo}}{{tagchip (printf "applies_to:%s" .)}}{{end}}</div></td>
  <td>{{.Headline}}</td>
  <td class="updated">{{ftime .Updated}}</td>
</tr>{{else}}<tr><td colspan="3" class="empty">none indexed — run <code>satelle reindex</code></td></tr>{{end}}{{end}}

{{define "workflowDetail"}}<div class="expbody">
  <h4>{{.Name}}</h4>
  {{if .Headline}}<div class="meta">{{.Headline}}</div>{{end}}
  <div class="wi-tags">{{if .Provenance}}{{tagchip (printf "provenance:%s" .Provenance)}}{{end}}{{if .Scope}}{{tagchip (printf "scope:%s" .Scope)}}{{end}}{{range .AppliesTo}}{{tagchip (printf "applies_to:%s" .)}}{{end}}</div>
  {{if .Source}}<div class="meta mono">source: {{.Source}}</div>{{end}}

  <h4>Route</h4>
  {{if .Route.Steps}}<div class="meta">the ordered steps to done — order is the workflow's, not the agent's</div>
  <ol class="route">{{range .Route.Steps}}<li class="route-step{{if .Terminal}} terminal{{end}}" data-state="{{.Status}}">
    <div class="route-head"><span class="wf-node{{if .Terminal}} terminal{{end}}">{{.Status}}</span>{{if .Obligation}}<span class="route-oblig">{{.Obligation}}</span>{{end}}</div>
    <div class="route-perf">{{if .Skills}}<span class="route-agent">{{if .Agent}}{{.Agent}}{{else}}in-loop{{end}}</span> runs {{range .Skills}}<span class="route-skill">@skill:{{.}}</span>{{end}}{{else if .Terminal}}<span class="route-none">terminal</span>{{else if .Agent}}<span class="route-agent">{{.Agent}}</span>{{else}}<span class="route-none">not performed</span>{{end}}</div>
    <div class="route-gates">{{if or .Reviewers .Skipped}}<span class="route-label">entry gated by</span>{{range .Reviewers}}<span class="wf-gate"{{if .ByTag}} title="on the route because the story carries {{join .ByTag " or "}}"{{else}} title="reviewer gate"{{end}}>{{.Skill}}{{if .ByTag}} <span class="route-bytag">by tag {{join .ByTag "|"}}</span>{{end}}</span>{{end}}{{range .Skipped}}<span class="wf-gate skipped" title="skipped — this story carries none of {{join .ByTag " or "}}">{{.Skill}} <span class="route-bytag">needs tag {{join .ByTag "|"}}</span></span>{{end}}{{else}}<span class="wf-gate ungated" title="no reviewer gates entry to this step">entry ungated</span>{{end}}</div>
    {{with .Advisor}}<div class="route-advisor">advisor: <span class="route-agent">{{.Agent}}</span>{{if .Skill}} under <span class="route-skill">@skill:{{.Skill}}</span>{{end}} — the orchestrator consults it and records the advice; nothing dispatches it</div>{{end}}
    {{with .Rework}}<div class="route-advisor">rework: consult <span class="route-agent">{{.Consult}}</span> for up to {{.Rounds}} round(s) — the orchestrator may open <code>satelle story rework</code>; the outcome is context, the entry gate still decides</div>{{end}}
  </li>{{end}}</ol>
  {{if .Route.Exits}}<div class="route-exits"><span class="route-label">exits (off-route)</span>{{range .Route.Exits}}<span class="route-exit"><span class="wf-node sm">{{.Status}}</span><span class="route-none">{{if .Park}}park — resumes to origin{{else}}terminal{{end}}</span>{{range .Gates}}<span class="wf-gate">{{.}}</span>{{end}}</span>{{end}}</div>{{end}}
  {{else}}<div class="empty">no route — this workflow declares no path to a terminal success state</div>{{end}}

  <h4>Definition</h4>
  <pre class="prose">{{.Body}}</pre>
</div>{{end}}

{{define "itemDetail"}}<div class="expbody">
  {{$isTask := eq (printf "%s" .Item.Kind) "task"}}
  {{if not .Standalone}}<a class="detail-link open-story" href="{{.Item.Kind}}/{{.Item.ID}}">Open {{.Item.Kind}} →</a>{{end}}
  <dl>
    <dt>Status</dt><dd><span class="badge s-{{.Item.Status}}">{{.Item.Status}}</span></dd>
    {{if not $isTask}}<dt>Priority</dt><dd>{{if .Item.Priority}}{{.Item.Priority}}{{else}}—{{end}}</dd>
    <dt>Category</dt><dd>{{if .Item.Category}}{{.Item.Category}}{{else}}—{{end}}</dd>{{end}}
    {{if .Item.ParentID}}<dt>Parent</dt><dd><a href="story/{{.Item.ParentID}}">{{.Item.ParentID}}</a></dd>{{end}}
    {{if .Item.Tags}}<dt>Tags</dt><dd class="wi-tags">{{range .Item.Tags}}{{tagchip .}}{{end}}</dd>{{end}}
    <dt>Updated</dt><dd>{{ftime .Item.UpdatedAt}}</dd>
  </dl>
  {{if .Item.Body}}<h4>{{if $isTask}}Work definition{{else}}Description{{end}}</h4><pre class="prose">{{.Item.Body}}</pre>{{end}}
  {{if .Item.AcceptanceCriteria}}<h4>Acceptance criteria</h4><pre class="prose">{{.Item.AcceptanceCriteria}}</pre>{{end}}
  {{if $isTask}}<h4>Runs</h4>
  {{if .Executions}}<ol class="run-list">{{range .Executions}}<li class="run run-s-{{.Status}}">
    <div class="run-head"><span class="run-id">{{.ID}}</span> <span class="badge s-{{.Status}}">{{.Status}}</span></div>
    <div class="run-meta">created {{ftime .CreatedAt}} · updated {{ftime .UpdatedAt}}</div>
    {{if .Output}}<pre class="run-output prose">{{.Output}}</pre>{{else}}<div class="run-noout">no output recorded</div>{{end}}
  </li>{{end}}</ol>{{else}}<div class="empty">No runs yet — create one with <code>satelle execution create --parent {{.Item.ID}}</code>.</div>{{end}}{{end}}
  {{with .Route}}<h4>Route</h4>
  <article class="doc-article route-doc" id="doc-route">{{.HTML}}</article>{{end}}
  {{if .Docs}}<h4>Documents</h4>
  <ul class="doc-list">{{range .Docs}}<li><details class="doc-item" id="{{.Anchor}}"><summary>{{.Name}}{{if .Type}} <span class="doc-item-type">{{.Type}}</span>{{end}}</summary><article class="doc-article">{{.HTML}}</article></details></li>{{end}}</ul>{{end}}
  <h4>Timeline</h4>
  {{if .Events}}<ol class="timeline">{{range .Events}}<li{{with evdot .Kind}} class="{{.}}"{{end}}>
    <div class="ev-kind">{{.Kind}}</div>
    <div class="ev-meta">{{ftime .CreatedAt}}{{if .Actor}} · {{.Actor}}{{end}}</div>
    {{if .DocName}}<div class="ev-doc">{{if .DocHref}}<a href="{{.DocHref}}">{{.DocName}}{{if .DocType}} <span class="doc-item-type">{{.DocType}}</span>{{end}}</a>{{else}}{{.DocName}}{{if .DocType}} <span class="doc-item-type">{{.DocType}}</span>{{end}}{{end}}</div>{{end}}
    {{if .Body}}<div class="ev-body">{{.Body}}</div>{{end}}
    {{if .Chips}}<div class="ev-chips">{{range .Chips}}<span class="chip chip-{{.Type}}">{{.Label}}</span>{{end}}</div>{{end}}
  </li>{{end}}</ol>{{else}}<div class="empty">No ledger events yet.</div>{{end}}
</div>{{end}}

{{define "workspace"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · workspace</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs"><a href="/">workspace</a> <span class="sep">/</span> <span class="cur">items</span></nav>
  <header class="app">
    <h1>workspace</h1>
    <div class="meta">{{len .Repos}} repos aggregated</div>
  </header>
  {{range .Repos}}<div class="meta ws-repo-line"><strong>{{.Name}}</strong> · {{.Path}}{{if .Err}} · unreadable: {{.Err}}{{else}} · {{len .Stories}} stories · {{len .Tasks}} tasks · {{len .Docs}} docs{{end}}</div>{{else}}<div class="empty">no repos registered — run <code>satelle workspace add</code></div>{{end}}
  {{if gt .TotalStories 0}}<table class="panel-table">
    <thead><tr><th>Project</th><th>ID</th><th>Title</th><th>Status</th></tr></thead>
    <tbody>{{range .Repos}}{{$repo := .Name}}{{range .Stories}}<tr><td>{{$repo}}</td><td class="id">{{.ID}}</td><td>{{.Title}}</td><td><span class="badge s-{{.Status}}">{{.Status}}</span></td></tr>{{end}}{{end}}</tbody>
  </table>{{end}}
  {{template "footer"}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}

{{define "projects"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>satelle · workspace</title>
<script>(function(){try{var t=localStorage.getItem('satelle-theme');if(t==='dark'||t==='light')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<base href="{{basehref}}">
{{template "favicon"}}
<link rel="stylesheet" href="/static/app.css">
</head>
<body data-page="projects">
{{template "topbar" .TopBar}}
<div class="wrap">
  <nav class="crumbs"><span class="cur">workspace</span></nav>
  <header class="app">
    <h1>workspace</h1>
    <div class="meta">{{len .Projects}} project{{if ne (len .Projects) 1}}s{{end}} in the workspace · <a href="help">help →</a></div>
  </header>
  {{range .Projects}}<a class="proj-card" href="{{.URL}}">
    <div class="proj-name">{{.Name}} <span class="proj-slug">/{{.Slug}}/</span></div>
    <div class="meta">{{.Path}}</div>
    <div class="meta">{{.Stories}} stories · {{.Tasks}} tasks · {{.Docs}} docs</div>
  </a>{{else}}<div class="empty">no projects registered — run <code>satelle workspace add</code></div>{{end}}
  {{range .Failed}}<div class="proj-card proj-failed">
    <div class="proj-name">{{.Name}} <span class="badge s-blocked">not serving</span></div>
    <div class="meta">{{.Path}}</div>
    <div class="meta">{{.Err}}</div>
  </div>{{end}}
  <article class="doc-article landing-help">
    <h2>Add a project</h2>
    <p>Register any repo and it appears here within a few seconds, served at <code>/&lt;slug&gt;/</code> — live, no restart:</p>
    <pre><code>satelle workspace add /path/to/repo</code></pre>
    <p>Stop serving one with <code>satelle workspace remove &lt;path&gt;</code>; list them with <code>satelle workspace list</code>.</p>
    <h2>Help &amp; updates</h2>
    <p><a href="help">Process guides →</a> · keep the binary current with <code>satelle update</code> (<code>--check</code> to peek first).</p>
  </article>
  {{template "footer"}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}

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
  {{template "footer"}}
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
  <div class="settings-note settings-readonly-note">Repo settings are read-only here — edit <code>.satelle/satelle.toml</code> directly (and commit it under the workflow) to change them. Machine-wide hosted server lives on <a href="/settings/global">global settings</a>. Overlay (<code>satelle.local.toml</code>) values are not shown here.</div>
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
  {{template "footer"}}
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
  <nav class="crumbs"><a href="/">workspace</a> <span class="sep">/</span> <span class="cur">global settings</span></nav>
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
  {{template "footer"}}
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
  <nav class="crumbs"><a href="{{basehref}}">project</a> <span class="sep">/</span> <a href="{{basehref}}#docs">docs</a> <span class="sep">/</span> <span class="cur">{{.Name}}</span></nav>
  <header class="app">
    <div class="kind-h">{{.Kind}}</div>
    <h1>{{.Name}}</h1>
    {{if .Headline}}<div class="meta">{{.Headline}}</div>{{end}}
    {{if or .Provenance .Source}}<div class="wi-tags">{{if .Provenance}}{{tagchip (printf "provenance:%s" .Provenance)}}{{end}}</div>{{end}}
    {{if .Source}}<div class="meta mono">source: {{.Source}}</div>{{end}}
  </header>
  <article class="doc-article">{{.HTML}}</article>
  {{template "footer"}}
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
  <nav class="crumbs"><a class="back-link" href="{{basehref}}?expand={{.Item.ID}}#{{tabof .Item.Kind}}" aria-label="Back to project">←</a> <a href="{{basehref}}">project</a> <span class="sep">/</span> <a href="{{basehref}}#{{tabof .Item.Kind}}">{{.Item.Kind}}</a> <span class="sep">/</span> <span class="cur">{{.Item.ID}}</span></nav>
  <header class="app">
    <div class="kind-h">{{.Item.Kind}}</div>
    <h1>{{.Item.Title}}</h1>
    <div class="meta">{{.Item.ID}}</div>
  </header>
  <div id="detail-live" data-kind="{{.Item.Kind}}" data-id="{{.Item.ID}}">{{template "itemDetail" .}}</div>
  {{template "footer"}}
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
  <nav class="crumbs"><a href="/">workspace</a> <span class="sep">/</span> <span class="cur">logs</span></nav>
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
  {{template "footer"}}
</div>
<script src="/static/app.js"></script>
</body>
</html>{{end}}
`
