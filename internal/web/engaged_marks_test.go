//go:build linux || darwin

package web

import (
	"encoding/json"
	"html"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// engagedFixture serves a partition (hosted when hosted is set) on workflow
// flow (alpha start, beta active, zed terminal) and fix (panel Drift, open
// start, done terminal), with seats in every place (sto_44a18f5b AC4):
//   - sto_live: this machine, session alive (bob);
//   - sto_unv: this machine, no session record (bob);
//   - sto_far: another machine, other-host (ann);
//   - sto_node: remote:nod_7 (bob);
//   - sto_pair: bob here and alive, ann on other-host, carl in the cloud;
//   - sto_drift: on the Drift panel, here and alive;
//   - sto_gone: this machine, owner killed;
//   - sto_fin: terminal, here and alive;
//   - sto_idle: no seat.
//
// It returns the project page's URL; SATELLE_HOME is this machine's home.
func engagedFixture(t *testing.T, hosted *mirror.HostedMeta) string {
	t.Helper()
	home := t.TempDir()
	machine, err := config.MachineID(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"s-live", "s-pair-bob", "s-drift", "s-fin"} {
		if err := seatlife.Own(home, s, procgroup.Self()); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, start := procgroup.ProbeIdentity(cmd.Process.Pid)
	dead := procgroup.Proc{PID: cmd.Process.Pid, Start: start}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := seatlife.Own(home, "s-gone", dead); err != nil {
		t.Fatal(err)
	}

	fix := mirror.Workflow{Name: "fix", Panel: "Drift", States: []mirror.WorkflowState{{Name: "open", Start: true}, {Name: "done", Terminal: true}}}
	doc := func(w mirror.Workflow) json.RawMessage {
		return raw(mirror.Doc{Kind: mirror.DocKindWorkflows, Name: w.Name, Source: ".satelle/workflows/" + w.Name + ".toml", Body: "name = \"" + w.Name + "\"", Workflow: &w})
	}
	story := func(id, wf, st string) json.RawMessage {
		return raw(mirror.Item{ID: id, Kind: "story", Title: "T " + id, Status: st, Workflow: wf, CreatedAt: activityT0, UpdatedAt: activityT0})
	}
	local := func(id, session, m, host, user string) json.RawMessage {
		return raw(mirror.Seat{ID: id + "/" + session, Item: id, Session: session, Location: "local:/repos/x", Machine: m, Host: host, User: user, BoundAt: activityT0})
	}
	at := func(id, session, loc, user string) json.RawMessage {
		return raw(mirror.Seat{ID: id + "/" + session, Item: id, Session: session, Location: loc, User: user, BoundAt: activityT0})
	}
	snap := mirror.Snapshot{
		RepoKey: "rk", Slug: "proj",
		Stories: []json.RawMessage{
			story("sto_live", "flow", "beta"), story("sto_unv", "flow", "beta"), story("sto_far", "flow", "beta"),
			story("sto_node", "flow", "beta"), story("sto_pair", "flow", "beta"), story("sto_gone", "flow", "beta"),
			story("sto_idle", "flow", "beta"), story("sto_fin", "flow", "zed"), story("sto_drift", "fix", "open"),
		},
		Seats: []json.RawMessage{
			local("sto_live", "s-live", machine, "this-host", "bob@example.com"),
			local("sto_unv", "s-unv", machine, "this-host", "bob@example.com"),
			local("sto_far", "s-far", "m-other", "other-host", "ann@example.com"),
			at("sto_node", "s-node", "remote:nod_7", "bob@example.com"),
			local("sto_pair", "s-pair-bob", machine, "this-host", "bob@example.com"),
			local("sto_pair", "s-pair-ann", "m-other", "other-host", "ann@example.com"),
			at("sto_pair", "s-pair-carl", "cloud:https://claude.ai/code/session_x", "carl@example.com"),
			local("sto_drift", "s-drift", machine, "this-host", "bob@example.com"),
			local("sto_gone", "s-gone", machine, "this-host", "bob@example.com"),
			local("sto_fin", "s-fin", machine, "this-host", "bob@example.com"),
		},
		Docs:     []json.RawMessage{doc(fixtureWorkflow), doc(fix)},
		Identity: raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p", Hosted: hosted}),
	}
	srv := mirrorWith(t, snap)
	t.Setenv("SATELLE_HOME", home)
	return srv.URL + "/r/proj/"
}

// markTitle is the hover of the mark reading text in a title cell
// (titleCellFor): the title of the nearest <span before >text<, unescaped. It
// fails the test when the mark is absent.
func markTitle(t *testing.T, cell, text string) string {
	t.Helper()
	at := strings.Index(cell, ">"+text+"<")
	if at < 0 {
		t.Errorf("no %q mark in %q", text, cell)
		return ""
	}
	span := strings.LastIndex(cell[:at], "<span")
	if span < 0 {
		t.Errorf("the %q mark is not a span: %q", text, cell)
		return ""
	}
	tag := cell[span:at]
	_, rest, ok := strings.Cut(tag, `title="`)
	if !ok {
		t.Errorf("the %q mark has no title: %q", text, tag)
		return ""
	}
	title, _, _ := strings.Cut(rest, `"`)
	return html.UnescapeString(title)
}

// markCount is the number of spans of class (alone or first) in cell.
func markCount(cell, class string) int {
	return len(regexp.MustCompile(`<span class="`+regexp.QuoteMeta(class)+`[" ]`).FindAllString(cell, -1))
}

var engagedRowRE = regexp.MustCompile(`data-engaged="([^"]*)"[^>]*data-expand-url="fragment/story/([^"]+)"`)

// engagedRows maps each row of html carrying data-engaged to its value.
func engagedRows(html string) map[string]string {
	out := map[string]string{}
	for _, m := range engagedRowRE.FindAllStringSubmatch(html, -1) {
		out[m[2]] = m[1]
	}
	return out
}

// sto_44a18f5b AC1-AC4: in a local and a hosted partition alike, every story
// row carries a green ◆ here for a live seat here, ◇ here for an unverified
// one, one mark per other place naming its user and place on hover, and
// nothing for a gone seat or a terminal story; the 'N engaged' and
// 'N elsewhere' badges count the Stories-tab rows whose data-engaged says so.
func TestEngagedMarksEveryPlace(t *testing.T) {
	for mode, hosted := range map[string]*mirror.HostedMeta{"local": nil, "hosted": hostedProbe()} {
		t.Run(mode, func(t *testing.T) {
			page := engagedFixture(t, hosted)
			body := httpGetBody(t, page)
			cell := func(id string) string {
				c := titleCellFor(body, id)
				if c == "" {
					t.Fatalf("%s is not listed", id)
				}
				return c
			}

			// AC1: here, verified and unverified; the Drift panel's row too.
			live := cell("sto_live")
			if markCount(live, "here-mark") != 1 || strings.Contains(live, "here-unverified") || markCount(live, "where-mark") != 0 {
				t.Errorf("sto_live is not marked ◆ here alone: %q", live)
			}
			if got := markTitle(t, live, "◆ here"); !strings.Contains(got, "engaged here by bob@example.com") {
				t.Errorf("sto_live's hover %q lacks its user", got)
			}
			unv := cell("sto_unv")
			if !strings.Contains(unv, `class="here-mark here-unverified"`) || markCount(unv, "where-mark") != 0 {
				t.Errorf("sto_unv is not marked ◇ here alone: %q", unv)
			}
			got := markTitle(t, unv, "◇ here")
			for _, want := range []string{"unverified: no session record on this machine", "by bob@example.com"} {
				if !strings.Contains(got, want) {
					t.Errorf("sto_unv's hover %q lacks %q", got, want)
				}
			}
			drift := section(body, "wf-drift")
			if c := titleCellFor(drift, "sto_drift"); !strings.Contains(c, ">◆ here<") {
				t.Errorf("sto_drift on the Drift panel is not marked here: %q", c)
			}
			for _, id := range []string{"sto_gone", "sto_fin", "sto_idle"} {
				c := cell(id)
				if strings.Contains(c, "here-mark") || strings.Contains(c, "where-mark") || strings.Contains(rowFor(body, id), "data-engaged") {
					t.Errorf("%s carries an engaged mark: %q %q", id, rowFor(body, id), c)
				}
			}

			// AC2: each place elsewhere, its user and location on hover.
			far := cell("sto_far")
			if markCount(far, "here-mark") != 0 || markCount(far, "where-mark") != 1 {
				t.Errorf("sto_far is not marked once elsewhere: %q", far)
			}
			got = markTitle(t, far, "◆ ann@other-host")
			for _, want := range []string{"engaged by ann@example.com", "on other-host (m-other)"} {
				if !strings.Contains(got, want) {
					t.Errorf("sto_far's hover %q lacks %q", got, want)
				}
			}
			node := cell("sto_node")
			if markCount(node, "here-mark") != 0 || markCount(node, "where-mark") != 1 {
				t.Errorf("sto_node is not marked once elsewhere: %q", node)
			}
			got = markTitle(t, node, "◆ remote:nod_7")
			for _, want := range []string{"engaged by bob@example.com", "at remote:nod_7"} {
				if !strings.Contains(got, want) {
					t.Errorf("sto_node's hover %q lacks %q", got, want)
				}
			}

			// AC4: two users and three places on one story, each mark its own.
			pair := cell("sto_pair")
			if markCount(pair, "here-mark") != 1 || markCount(pair, "where-mark") != 2 {
				t.Errorf("sto_pair lacks one here mark and two where-marks: %q", pair)
			}
			if a, c := strings.Index(pair, ">◆ ann@other-host<"), strings.Index(pair, ">◆ cloud<"); a < 0 || c < 0 || a > c {
				t.Errorf("sto_pair's where-marks are not ann@other-host then cloud: %q", pair)
			}
			for mark, w := range map[string]struct{ want, not []string }{
				"◆ here":           {[]string{"engaged here by bob@example.com"}, []string{"ann@", "carl@"}},
				"◆ ann@other-host": {[]string{"engaged by ann@example.com", "on other-host (m-other)"}, []string{"bob@", "carl@"}},
				"◆ cloud":          {[]string{"engaged by carl@example.com", "at cloud:https://claude.ai/code/session_x"}, []string{"bob@", "ann@"}},
			} {
				got := markTitle(t, pair, mark)
				for _, s := range w.want {
					if !strings.Contains(got, s) {
						t.Errorf("sto_pair's %s hover %q lacks %q", mark, got, s)
					}
				}
				for _, s := range w.not {
					if strings.Contains(got, s) {
						t.Errorf("sto_pair's %s hover %q names %q", mark, got, s)
					}
				}
			}

			// AC3: data-engaged on each row, and both counts from the same rule.
			rows := engagedRows(section(body, "stories"))
			want := map[string]string{
				"sto_live": "here", "sto_unv": "here", "sto_far": "elsewhere",
				"sto_node": "elsewhere", "sto_pair": "here,elsewhere",
			}
			if len(rows) != len(want) {
				t.Errorf("Stories rows engaged %v, want %v", rows, want)
			}
			for id, w := range want {
				if rows[id] != w {
					t.Errorf("%s data-engaged = %q, want %q", id, rows[id], w)
				}
			}
			var here, elsewhere []string
			for id, v := range rows {
				parts := strings.Split(v, ",")
				if slices.Contains(parts, "here") {
					here = append(here, id)
				}
				if slices.Contains(parts, "elsewhere") {
					elsewhere = append(elsewhere, id)
				}
			}
			slices.Sort(here)
			slices.Sort(elsewhere)
			for name, h := range map[string]string{"page": body, "fragment": httpGetBody(t, page+"fragment/engagement")} {
				for _, w := range []string{
					`data-engagement-count="3"`, `title="engaged here: ` + strings.Join(here, ", ") + `"`, ">3 engaged<",
					`data-elsewhere-count="3"`, `title="engaged elsewhere: ` + strings.Join(elsewhere, ", ") + `"`, ">3 elsewhere<",
					`title="engaged here: sto_live, sto_pair, sto_unv"`, `title="engaged elsewhere: sto_far, sto_node, sto_pair"`,
				} {
					if !strings.Contains(h, w) {
						t.Errorf("%s: the badges lack %s", name, w)
					}
				}
			}
		})
	}
}

// sto_44a18f5b AC3: with no seats anywhere, neither the engaged nor the
// elsewhere badge shows, on the page or its fragment, and no row is marked.
func TestNoSeatsNoEngagedBadges(t *testing.T) {
	page := hostedPartition(t, nil)
	for name, h := range map[string]string{"page": httpGetBody(t, page), "fragment": httpGetBody(t, page+"fragment/engagement")} {
		for _, not := range []string{"n-engaged", "n-elsewhere", "data-engaged"} {
			if strings.Contains(h, not) {
				t.Errorf("%s shows %s with no seats:\n%s", name, not, h)
			}
		}
	}
}

// sto_44a18f5b AC1: the here mark is green in both themes. The stylesheet
// colours .here-mark with --ok, no rule recolours it (the unverified one
// included), --ok is a green in :root and in the dark theme, and the rendered
// mark carries the class.
func TestHereMarkIsGreen(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	if want := ".here-mark { margin-left: 6px; font-size: 10.5px; font-weight: 600; color: var(--ok); white-space: nowrap; }"; !strings.Contains(css, want) {
		t.Errorf("app.css lacks %q", want)
	}
	colour := regexp.MustCompile(`(?:^|[;{\s])color:\s*([^;}]+)`)
	for _, m := range regexp.MustCompile(`([^{}]+)\{([^}]*)\}`).FindAllStringSubmatch(css, -1) {
		if !strings.Contains(m[1], ".here-mark") {
			continue
		}
		if c := colour.FindStringSubmatch(m[2]); c != nil && strings.TrimSpace(c[1]) != "var(--ok)" {
			t.Errorf("%s colours the here mark %s", strings.TrimSpace(m[1]), c[1])
		}
	}
	for _, block := range []string{":root {", `[data-theme="dark"] {`} {
		_, rest, ok := strings.Cut(css, block)
		if !ok {
			t.Fatalf("app.css has no %s block", block)
		}
		rest, _, _ = strings.Cut(rest, "}")
		m := regexp.MustCompile(`--ok:\s*#([0-9a-fA-F]{6})`).FindStringSubmatch(rest)
		if m == nil {
			t.Errorf("%s declares no hex --ok", block)
			continue
		}
		ch := func(i int) int64 { v, _ := strconv.ParseInt(m[1][i:i+2], 16, 32); return v }
		if r, g, bl := ch(0), ch(2), ch(4); g <= r || g <= bl {
			t.Errorf("%s --ok #%s is not green", block, m[1])
		}
	}
	body := httpGetBody(t, engagedFixture(t, nil))
	if c := titleCellFor(body, "sto_live"); !strings.Contains(c, `<span class="here-mark" title=`) {
		t.Errorf("sto_live's mark does not carry the green class: %q", c)
	}
}
