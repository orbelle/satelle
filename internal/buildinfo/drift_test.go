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
	// The card's case: the running satelled is behind the installed one, and the
	// CLI matches the installed satelled. Restart only.
	got := Drift(c1, r0, i1)
	if len(got) != 1 || got[0].Remedy != "satelle service restart" || !slices.Equal(got[0].Lines, []string{"running", "installed"}) {
		t.Errorf("card case: %+v", got)
	}
	// The CLI's version differs from the installed satelled's: update.
	got = Drift(c1, r0, r0)
	if len(got) != 1 || got[0].Remedy != "satelle update" || !slices.Equal(got[0].Lines, []string{"cli", "installed"}) {
		t.Errorf("CLI differs: %+v", got)
	}
	// Both: restart first, then update.
	got = Drift(line("satelle", "1.0.45"), r0, i1)
	if len(got) != 2 || got[0].Remedy != "satelle service restart" || got[1].Remedy != "satelle update" {
		t.Errorf("both: %+v", got)
	}
	// A CLI line that names no build is never compared.
	for _, cli := range []string{"", "unknown (no satelle at /x)"} {
		if got := Drift(cli, r0, r0); len(got) != 0 {
			t.Errorf("cli %q compared: %+v", cli, got)
		}
	}
	// An installed satelled replaced after it last ran still warns restart.
	got = Drift(c0, r0, "unknown (/x/satelled changed since it last ran)")
	if len(got) != 1 || got[0].Remedy != "satelle service restart" {
		t.Errorf("changed installed: %+v", got)
	}
	// Dev builds compare on the base version Info.String prints.
	dev := Info{Name: "satelle", Version: "1.0.43+abc-dirty", Commit: "abc", BuildTime: "t"}.String()
	if got := Drift(dev, r0, r0); len(got) != 0 {
		t.Errorf("dev marker compared: %+v", got)
	}
}
