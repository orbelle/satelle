package hook

import (
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/gitfacts"
)

const (
	WiringInstalled    = "installed"
	WiringNotInstalled = "not installed"
	WiringIgnored      = "installed but git-ignored"
)

type Wiring struct {
	Harness string `json:"harness"`
	File    string `json:"file"`
	State   string `json:"state"`
	Rule    string `json:"rule,omitempty"`
	Present bool   `json:"-"`
}

func (w Wiring) Wired() bool { return w.State != WiringNotInstalled }

func WiringOf(root string) []Wiring {
	out := make([]Wiring, 0, len(Harnesses))
	var installed []string
	for _, h := range Harnesses {
		rel, _ := File(h)
		w := Wiring{Harness: h, File: rel, State: WiringNotInstalled}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			w.Present = true
		}
		if Installed(root, h) {
			w.State = WiringInstalled
			installed = append(installed, rel)
		}
		out = append(out, w)
	}
	hidden, err := gitfacts.Repo{Dir: root}.IgnoredBy(installed...)
	if err != nil {
		return out
	}
	for i, w := range out {
		if rule, ok := hidden[w.File]; ok {
			out[i].State, out[i].Rule = WiringIgnored, rule
		}
	}
	return out
}
