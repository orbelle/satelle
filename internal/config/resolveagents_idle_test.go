package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestResolveCarriesIdleTimeoutAcrossTiers pins sty_407158e4: idle_timeout is an
// execution field, so a profile may supply it, a chain folds it outermost-wins,
// and a repo value wins over all of them with source attribution.
func TestResolveCarriesIdleTimeoutAcrossTiers(t *testing.T) {
	const cat = `
[profiles.base]
role = "reviewer"
idle_timeout = "9m"

[profiles.child]
profile = "base"
idle_timeout = "7m"

[profiles.plain]
role = "reviewer"
`
	cases := []struct {
		name, repo, wantVal, wantSrc string
	}{
		{"repo over profile", "[reviewer]\nprofile = \"plain\"\nidle_timeout = \"5s\"\n", "5s", SourceRepo},
		{"profile supplies", "[reviewer]\nprofile = \"base\"\n", "9m", SourceProfile("base")},
		{"chain outermost wins", "[reviewer]\nprofile = \"child\"\n", "7m", SourceProfile("child")},
		{"repo over chain", "[reviewer]\nprofile = \"child\"\nidle_timeout = \"3s\"\n", "3s", SourceRepo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, prov, err := ResolveAgents(repoAgents(t, tc.repo), catalog(t, cat))
			if err != nil {
				t.Fatal(err)
			}
			if got.Reviewer.IdleTimeout != tc.wantVal {
				t.Errorf("idle_timeout = %q, want %q", got.Reviewer.IdleTimeout, tc.wantVal)
			}
			if s := prov.Source("reviewer", "idle_timeout"); s != tc.wantSrc {
				t.Errorf("source = %q, want %q", s, tc.wantSrc)
			}
		})
	}
}

// TestWorkspaceCarriesIdleTimeout: the workspace tier supplies idle_timeout
// when the repo is silent, and the repo wins when it states one.
func TestWorkspaceCarriesIdleTimeout(t *testing.T) {
	ws := wsAgents(t, "[reviewer]\nrole = \"reviewer\"\nidle_timeout = \"7m\"\n")
	got, prov, err := ResolveAgentsLayered(repoAgents(t, "[reviewer]\nrole = \"reviewer\"\n"), ws, GlobalAgentsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Reviewer.IdleTimeout != "7m" || prov.Source("reviewer", "idle_timeout") != SourceWorkspace {
		t.Errorf("workspace: %q from %q", got.Reviewer.IdleTimeout, prov.Source("reviewer", "idle_timeout"))
	}
	got, prov, err = ResolveAgentsLayered(repoAgents(t, "[reviewer]\nrole = \"reviewer\"\nidle_timeout = \"5s\"\n"), ws, GlobalAgentsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Reviewer.IdleTimeout != "5s" || prov.Source("reviewer", "idle_timeout") != SourceRepo {
		t.Errorf("repo: %q from %q", got.Reviewer.IdleTimeout, prov.Source("reviewer", "idle_timeout"))
	}
}

// TestGlobalRoleCarriesIdleTimeout: a [roles] default reached through
// use_global_roles supplies idle_timeout with a global-role source, and a repo
// value wins over it.
func TestGlobalRoleCarriesIdleTimeout(t *testing.T) {
	cat := catalog(t, `
[profiles.role-default]
role = "reviewer"
idle_timeout = "8m"

[roles]
reviewer = "role-default"
`)
	const optIn = "[defaults]\nuse_global_roles = true\n\n[reviewer]\nrole = \"reviewer\"\n"
	cases := []struct {
		name, repo, wantVal, wantSrc string
	}{
		{"global role supplies", optIn, "8m", SourceGlobalRole("role-default")},
		{"repo over global role", optIn + "idle_timeout = \"5s\"\n", "5s", SourceRepo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, prov, err := ResolveAgents(repoAgents(t, tc.repo), cat)
			if err != nil {
				t.Fatal(err)
			}
			if got.Reviewer.IdleTimeout != tc.wantVal {
				t.Errorf("idle_timeout = %q, want %q", got.Reviewer.IdleTimeout, tc.wantVal)
			}
			if s := prov.Source("reviewer", "idle_timeout"); s != tc.wantSrc {
				t.Errorf("source = %q, want %q", s, tc.wantSrc)
			}
		})
	}
}

// overlayExcluded lists toml-tagged AgentBinding fields overlayBinding
// deliberately does not carry, each with its reason.
var overlayExcluded = map[string]string{
	"harness":           "retired alias, migrated by MigrateAgents",
	"inject_principles": "retired alias, migrated by MigrateAgents",
	"profile":           "identity: set by the resolver after the overlay, not overlaid",
	"collect_doc":       "repo-only: names the story document this repo's gates read; overlayBinding carries the repo tier's (TestOverlayBindingCarriesCollectDoc), a catalog profile may not",
}

func TestOverlayBindingCarriesCollectDoc(t *testing.T) {
	out, src := overlayBinding(AgentBinding{Model: "m"}, nil, AgentBinding{CollectDoc: "ac-evidence"}, "R")
	if out.CollectDoc != "ac-evidence" || out.Model != "m" || src["collect_doc"] != "R" {
		t.Fatalf("overlay = %+v %v, want collect_doc carried and the lower tier kept", out, src)
	}
	out, _ = overlayBinding(out, src, AgentBinding{}, "U")
	if out.CollectDoc != "ac-evidence" {
		t.Errorf("an empty tier must not clear collect_doc: %+v", out)
	}
}

// TestOverlayBindingCarriesEveryScalarField fails when a toml-tagged
// AgentBinding field is added without overlayBinding (and the profile key
// allowlist) carrying it — the drift that made idle_timeout inert on
// profile-backed bindings.
func TestOverlayBindingCarriesEveryScalarField(t *testing.T) {
	typ := reflect.TypeOf(AgentBinding{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("toml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if _, skip := overlayExcluded[tag]; skip {
			continue
		}
		t.Run(tag, func(t *testing.T) {
			if !globalAgentsBindingKeys[tag] {
				t.Errorf("%s is not in globalAgentsBindingKeys: a profile cannot carry it", tag)
			}
			var top AgentBinding
			fv := reflect.ValueOf(&top).Elem().Field(i)
			switch f.Type.Kind() {
			case reflect.String:
				fv.SetString("x-" + tag)
			case reflect.Map:
				switch f.Type.Elem().Kind() {
				case reflect.String:
					fv.Set(reflect.ValueOf(map[string]string{"k": "v"}))
				case reflect.Interface:
					fv.Set(reflect.ValueOf(map[string]any{"k": "v"}))
				case reflect.Map: // cloud: provider -> its table
					fv.Set(reflect.ValueOf(map[string]map[string]string{"k": {"k": "v"}}))
				default:
					t.Fatalf("unhandled map element kind for %s, extend overlayBinding and this test", tag)
				}
			default:
				t.Fatalf("unhandled field kind %s for %s, extend overlayBinding and this test", f.Type.Kind(), tag)
			}
			out, src := overlayBinding(AgentBinding{}, nil, top, "T")
			if reflect.ValueOf(out).Field(i).IsZero() {
				t.Errorf("overlayBinding dropped %s", tag)
			}
			if src[tag] != "T" {
				t.Errorf("source for %s = %q, want T", tag, src[tag])
			}
		})
	}
}
