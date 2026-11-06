package web

import (
	"regexp"
	"strings"
	"testing"
)

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// decls is the declarations of the rule whose selector, at a line start, is
// exactly selector.
func decls(t *testing.T, css, selector string) map[string]string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(selector) + `\s*\{([^}]*)\}`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("no %s rule in the served stylesheet", selector)
	}
	out := map[string]string{}
	for _, d := range strings.Split(cssComment.ReplaceAllString(m[1], ""), ";") {
		k, v, ok := strings.Cut(d, ":")
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// TestSharedLayoutServedCSS pins the layout satelled shares with satelle.dev
// (sto_99aa0122, satelle-server sto_1006a260): a change here must be made in
// both apps.
func TestSharedLayoutServedCSS(t *testing.T) {
	srv := mirrorWith(t, fixtureSnapshot())
	css := httpGetBody(t, srv.URL+"/static/app.css")
	want := map[string]map[string]string{
		":root":         {"--content-max": "1600px", "--gutter-mobile": "1rem"},
		".wrap":         {"width": "100%", "max-width": "var(--content-max)", "margin": "0 auto", "padding": "28px 24px 64px"},
		".topbar-inner": {"width": "100%", "max-width": "var(--content-max)", "margin": "0 auto", "padding": "12px 24px"},
		".doc-article":  {"max-width": "75ch"},
	}
	for sel, props := range want {
		got := decls(t, css, sel)
		for k, v := range props {
			if got[k] != v {
				t.Errorf("%s { %s: %q }, want %q", sel, k, got[k], v)
			}
		}
	}
	if strings.Contains(css, "--content-w") {
		t.Error("the stylesheet still carries --content-w")
	}
	_, block, ok := strings.Cut(css, "@media (max-width: 640px) {")
	if !ok {
		t.Fatal("no literal @media (max-width: 640px) block")
	}
	block, _, _ = strings.Cut(block, "\n}")
	m := regexp.MustCompile(`([^{}]*)\{([^}]*)\}`).FindAllStringSubmatch(block, -1)
	found := false
	for _, r := range m {
		sels := strings.Split(r[1], ",")
		has := map[string]bool{}
		for _, s := range sels {
			has[strings.TrimSpace(s)] = true
		}
		if has[".wrap"] && has[".topbar-inner"] {
			found = true
			body := r[2]
			for _, p := range []string{"padding-left: var(--gutter-mobile)", "padding-right: var(--gutter-mobile)"} {
				if !strings.Contains(body, p) {
					t.Errorf("the 640px rule lacks %s", p)
				}
			}
		}
	}
	if !found {
		t.Error("the 640px block has no rule for .wrap and .topbar-inner")
	}
}
