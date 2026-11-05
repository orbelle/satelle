package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

func TestSettingsPrintAndSet(t *testing.T) {
	isolate(t)
	t.Setenv(config.EnvServerEndpoint, "")
	out := must(t, "settings")
	if !strings.Contains(out, `service.port      ""  (in effect: 8787)`) {
		t.Errorf("settings:\n%s", out)
	}
	must(t, "settings", "set", "server", "https://example.test/")
	must(t, "settings", "set", "service.port", "9911")
	must(t, "settings", "set", "service.addr", "127.0.0.1")
	must(t, "settings", "set", "service.endpoint", "http://127.0.0.1:9911")
	g, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.Hosted.Server != "https://example.test" || g.Service.Port != 9911 || g.Service.Addr != "127.0.0.1" || g.Service.Endpoint != "http://127.0.0.1:9911" {
		t.Errorf("saved %+v", g)
	}
	out = must(t, "settings")
	if !strings.Contains(out, `service.port      "9911"`) || !strings.Contains(out, `server            "https://example.test"`) {
		t.Errorf("settings after set:\n%s", out)
	}
	must(t, "settings", "set", "service.port", "")
	if g, _ := config.LoadGlobal(); g.Service.ResolvePort() != config.DefaultWebPort {
		t.Errorf("an empty value left port %d", g.Service.Port)
	}
	for _, args := range [][]string{{"nope", "x"}, {"service.port", "99999"}, {"server", "not a url"}} {
		if _, err := run(t, "", append([]string{"settings", "set"}, args...)...); err == nil {
			t.Errorf("settings set %v was accepted", args)
		}
	}
}
