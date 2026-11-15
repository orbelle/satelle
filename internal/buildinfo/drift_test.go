package buildinfo

import (
	"slices"
	"testing"
)

func line(name, version string) string {
	return Info{Name: name, Version: version, Commit: "aaaaaaaaaaaa", BuildTime: "t"}.String()
}

func TestDrift(t *testing.T) {
	r0 := line("satelled", "1.0.43")
	i1 := line("satelled", "1.0.44")
	c1 := line("satelle", "1.0.44")
	c0 := line("satelle", "1.0.43")

	if got := Drift(c0, r0, r0); len(got) != 0 {
		t.Errorf("everything matching: %v", got)
	}
	got := Drift(c1, r0, i1)
	if len(got) != 1 || got[0].Remedy != "satelle service restart" || !slices.Equal(got[0].Lines, []string{"running", "installed"}) {
		t.Errorf("card case: %+v", got)
	}
	got = Drift(c1, r0, r0)
	if len(got) != 1 || got[0].Remedy != "satelle update" || !slices.Equal(got[0].Lines, []string{"cli", "installed"}) {
		t.Errorf("CLI differs: %+v", got)
	}
	got = Drift(line("satelle", "1.0.45"), r0, i1)
	if len(got) != 2 || got[0].Remedy != "satelle service restart" || got[1].Remedy != "satelle update" {
		t.Errorf("both: %+v", got)
	}
	for _, cli := range []string{"", "unknown (no satelle at /x)"} {
		if got := Drift(cli, r0, r0); len(got) != 0 {
			t.Errorf("cli %q compared: %+v", cli, got)
		}
	}
	got = Drift(c0, r0, "unknown (/x/satelled changed since it last ran)")
	if len(got) != 1 || got[0].Remedy != "satelle service restart" {
		t.Errorf("changed installed: %+v", got)
	}
	dev := Info{Name: "satelle", Version: "1.0.43+abc-dirty", Commit: "abc", BuildTime: "t"}.String()
	if got := Drift(dev, r0, r0); len(got) != 0 {
		t.Errorf("dev marker compared: %+v", got)
	}
}
