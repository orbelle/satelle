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
	RepoRoot          string
	ProjectName       string
	DBPath            string
	Stories           []rowVM
	Panels            []panelVM
	BacklogCount      int
	EngagementCount   int
	EngagedStoryIDs   []string
	ElsewhereCount    int
	ElsewhereStoryIDs []string
	Tasks             []rowVM
	Queues            []queueVM
	Activity          []activityRowVM
	DocKinds          []kindGroup
	Workflows         []workflowRowVM
	Uptime            string
	Theme             string
	TopBar            topBar
	Projects          []crumbProject
	LastIngest        time.Time
	Stale             bool
	SyncLastSuccess   time.Time
	SyncReason        string
	SyncLocal         bool
	SyncStore         string
	SyncUpgrade       string
	SyncHeld          []syncHeldVM
	Hosted            *hostedLinksVM
	TagFacets         string
	TagFilterExample  string
}

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
	User   *topBarUser
	Active string
	Builds buildsVM
}

type rowVM struct {
	Item
	StateClass string
	Engaged    *engagementVM
	Assignees  []assigneeVM
	Progress   []stageVM
	TagColours map[string]string
}

type engagementVM struct {
	Here      *markVM
	Elsewhere []markVM
}

func (e *engagementVM) Attr() string {
	var parts []string
	if e.Here != nil {
		parts = append(parts, "here")
	}
	if len(e.Elsewhere) > 0 {
		parts = append(parts, "elsewhere")
	}
	return strings.Join(parts, ",")
}

func (e *engagementVM) Words() string {
	if e == nil {
		return ""
	}
	var parts []string
	if e.Here != nil {
		parts = append(parts, e.Here.Title)
	}
	for _, m := range e.Elsewhere {
		parts = append(parts, m.Title)
	}
	return strings.Join(parts, "; ")
}

type markVM struct {
	Label, Title string
	Unverified   bool
}

type assigneeVM struct {
	User    string
	Working bool
}

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
	Item          Item
	StateClass    string
	TagColours    map[string]string
	BackTab       string
	ParentKind    string
	Assignees     []assigneeVM
	Children      []childLink
	Progress      []stageVM
	Route         routeVM
	Presentations []presentationVM
	Attachments   []attachmentVM
	Events        []eventVM
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
	DocName string
	DocHref string
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
	FieldID  string
	Label    string
	Help     string
	Value    string
	SectHead string
}

type settingsData struct {
	Rows     []settingsRowVM
	TopBar   topBar
	RepoRoot string
	MirrorRO bool
}

func sectionLabel(s string) string {
	if s == "" {
		return "General"
	}
	return strings.ToUpper(s[:1]) + s[1:]
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

func newTopBar(ctx context.Context, st *mirror.Store, active string) topBar {
	return topBar{Uptime: formatUptime(time.Since(serverStart)), Active: active, User: signedIn(ctx, st), Builds: newBuilds()}
}

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
