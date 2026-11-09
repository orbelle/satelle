// Package web is satelle's push-fed read-only web UI (mirror server).
// The live verb-dispatch Server (web.New) was removed in sty_80233c10 —
// production uses NewMirror only. Shared view-model types and pure helpers
// stay here with the embedded static assets and templates.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

// serverStart marks when the web service process came up.
var serverStart = time.Now()

func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("up %ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("up %dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("up %dh %dm", h, m)
}

func globalTheme() string {
	gc, err := config.LoadGlobal()
	if err != nil {
		return ""
	}
	if gc.UI.Theme == "dark" || gc.UI.Theme == "light" {
		return gc.UI.Theme
	}
	return ""
}

func getTheme(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	t := globalTheme()
	if t == "" {
		t = "light"
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"theme": t})
}

type pageData struct {
	RepoRoot    string
	ProjectName string
	DBPath      string
	Stories     []rowVM
	// Panels are the tabs of workflows that declare a panel: their stories
	// are listed there instead of on Stories (sto_a1681009).
	Panels []panelVM
	// BacklogCount is the number of stories in backlog, the Stories tab's
	// 'N backlog' badge; rendered at page load and refetched live.
	BacklogCount int
	// EngagementCount is the number of stories in a non-terminal state that hold
	// a non-stale seat; a task run's seat is not counted (Activity shows it).
	// Always rendered in project chrome — 0 is visible, not hidden (sty_01ba9482).
	EngagementCount int
	// EngagedStoryIDs lists those seated stories' ids (sorted); empty when count is 0.
	EngagedStoryIDs []string
	Tasks           []rowVM
	Queues          []queueVM
	Activity        []activityRowVM // stories and task runs being worked now
	DocKinds        []kindGroup
	Workflows       []workflowRowVM
	Uptime          string
	Theme           string
	TopBar          topBar
	Projects        []crumbProject // this machine's projects, for the breadcrumb switcher
	// LastIngest is when this partition's state was last confirmed against the
	// repo, by a push or the automatic sync pass (sto_128803b3), rendered as
	// elapsed time so the page never presents a frame satelled is behind on as
	// current (sty_e6e467fe, presented per sty_226a661e).
	LastIngest time.Time
	// Stale is set when LastIngest is older than mirror.StaleAfter (or absent):
	// no sync pass has confirmed the mirror. The header then shows the stale
	// marker beside the elapsed time.
	Stale bool
	// SyncLastSuccess / SyncReason / SyncStore are the recorded outcome of the
	// last automatic ledger sync pass (syncstate's sync group, sto_5c706973);
	// SyncLocal is set when the repo's store is local.
	SyncLastSuccess time.Time
	SyncReason      string
	SyncLocal       bool
	SyncStore       string
	// SyncHeld are the stories the last push held until their code is on the
	// remote (sty_c203fd62). Holding is intended while a story is in flight, so
	// they are shown apart from SyncReason, never as sync failing.
	SyncHeld []syncHeldVM
	// Hosted, set when the server is the repo's store of record, links the
	// server's project page; Stories still lists every story in the local store.
	Hosted *hostedLinksVM
}

// hostedLinksVM links a hosted-mode page to the server's project page. Only
// pages the server serves are linked (sty_9ece9394).
type hostedLinksVM struct {
	Project string
}

type crumbProject struct {
	Name      string
	Slug      string
	Path      string
	Current   bool
	Ambiguous bool
}

type topBar struct {
	Uptime string
	// User is the user this machine's CLI is signed in as, or nil when it is
	// signed out: the top bar shows it as an avatar.
	User *topBarUser
	// Active names the current nav item so its link renders accent (DS SiteHeader):
	// "home", "projects", "help", or "" for a page no nav item represents.
	Active string
	// Builds is the footer's build lines: the installed satelle CLI, the
	// running satelled and the installed satelled, with any disagreement.
	Builds buildsVM
}

type rowVM struct {
	Item
	// StateClass is the status's class in the item's workflow: start,
	// terminal, park or active ("" when the workflow was not pushed).
	StateClass string
	// Here marks a story engaged at this checkout in a hosted-mode partition. It
	// is a mark on the row, never a filter (sty_6ce071dc).
	Here bool
	// Assignees is every user assigned the item, each once: its explicit
	// assignees, then each user holding a working seat (assigneesOf).
	Assignees []assigneeVM
}

// assigneeVM is one assignee; Working marks a user holding a working seat.
type assigneeVM struct {
	User    string
	Working bool
}

// panelVM is one workflow panel's tab and rows: Topic is its tab and fragment
// key ("wf-" and the name's slug), Open the count of its rows not in a
// terminal state, the tab's count and alert.
type panelVM struct {
	Name  string
	Topic string
	Rows  []rowVM
	Open  int
}

type docRowVM struct {
	Name     string
	Headline string
	ModTime  time.Time
	Source   string
}

type kindGroup struct {
	Kind string
	Docs []docRowVM
}

type detailData struct {
	Item       Item
	StateClass string
	// BackTab is the project tab the item is listed on when it is not its
	// kind's own: a workflow panel's topic; "" means the kind's tab.
	BackTab       string
	ParentKind    string // the kind of the parent item, "" when not pushed
	Assignees     []assigneeVM
	Children      []childLink
	Route         routeVM
	Presentations []presentationVM
	Attachments   []attachmentVM
	Events        []eventVM // newest first
	TopBar        topBar
	Standalone    bool
}

type chipVM struct {
	Type  string
	Label string
}

type eventVM struct {
	Entry
	Chips   []chipVM
	DocName string // set for an attachment event — names the document on the timeline
	DocHref string // relative "<kind>/<id>#doc-<slug>" when the doc is present; else empty
}

type helpTopic struct {
	Name  string
	Title string
	HTML  template.HTML
}

type docPageData struct {
	TopBar   topBar
	Kind     string
	Name     string
	Headline string
	HTML     template.HTML
	Source   string
	// Workflow is set when the doc is a pushed workflow: the page draws it.
	Workflow *routeVM
}

type helpPageData struct {
	Topics []helpTopic
	TopBar topBar
}

type topBarUser struct {
	Name    string
	Email   string
	Initial string
}

type settingsRowVM struct {
	FieldID  string // the config key, shown as the row's monospace id
	Label    string
	Help     string
	Value    string
	SectHead string // non-empty on the first row of a new section group
}

type settingsData struct {
	Rows     []settingsRowVM
	TopBar   topBar
	RepoRoot string
	// MirrorRO hides the global-settings link (no write surface on push-fed serve).
	MirrorRO bool
}

func sectionLabel(s string) string {
	if s == "" {
		return "General"
	}
	return strings.ToUpper(s[:1]) + s[1:] // "hosted" → "Hosted", "gate" → "Gate"
}

func eventChips(e Entry) []chipVM {
	var p struct {
		Result     string `json:"result"`
		DurationMS int64  `json:"duration_ms"`
		TokensIn   int    `json:"tokens_in"`
		TokensOut  int    `json:"tokens_out"`
		Model      string `json:"model"`
	}
	if len(e.Payload) == 0 || json.Unmarshal(e.Payload, &p) != nil {
		return nil
	}
	var chips []chipVM
	if p.Result != "" {
		chips = append(chips, chipVM{Type: "outcome", Label: p.Result})
	}
	if p.DurationMS > 0 {
		chips = append(chips, chipVM{Type: "walltime", Label: humanMs(p.DurationMS)})
	}
	if n := p.TokensIn + p.TokensOut; n > 0 {
		chips = append(chips, chipVM{Type: "tokens", Label: humanTokens(n) + " tok"})
	}
	if p.Model != "" {
		chips = append(chips, chipVM{Type: "model", Label: p.Model})
	}
	return chips
}

func humanMs(ms int64) string {
	if ms <= 0 {
		return ""
	}
	if ms < 60000 {
		return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + "s"
	}
	return strconv.FormatInt(ms/60000, 10) + "m"
}

func humanTokens(n int) string {
	s := strconv.Itoa(n)
	if n < 1000 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func stripFrontmatter(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return strings.TrimSpace(s)
	}
	if end := strings.Index(s[4:], "\n---"); end >= 0 {
		rest := s[4+end+len("\n---"):]
		return strings.TrimSpace(strings.TrimPrefix(rest, "\n"))
	}
	return strings.TrimSpace(s)
}

// newTopBar builds the top bar: uptime, the active nav item and the user this
// machine's CLI is signed in as (from its latest push; nil when signed out).
func newTopBar(ctx context.Context, st *mirror.Store, active string) topBar {
	return topBar{Uptime: formatUptime(time.Since(serverStart)), Active: active, User: signedIn(ctx, st), Builds: newBuilds()}
}

// signedIn is the avatar for the user the CLI's latest push names: the name's
// first letter, with name and email.
func signedIn(ctx context.Context, st *mirror.Store) *topBarUser {
	u := st.SignedIn(ctx)
	if u == nil {
		return nil
	}
	label := u.Name
	if label == "" {
		label = u.Email
	}
	r, _ := utf8.DecodeRuneInString(label)
	return &topBarUser{Name: label, Email: u.Email, Initial: string(unicode.ToUpper(r))}
}

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
