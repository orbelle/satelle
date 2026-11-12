package hook

import (
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/gitfacts"
)

// The wiring states of a harness in a repo, as satelle status prints them.
const (
	WiringInstalled    = "installed"
	WiringNotInstalled = "not installed"
	WiringIgnored      = "installed but git-ignored"
)

// Wiring is one harness's hook file in a repo and whether satelle's hooks are
// in it. Rule is the ignore rule hiding an installed file from git
// (`<source>:<line>: <pattern>`), so a clone would not get it. Present is
// whether the file exists at all.
type Wiring struct {
	Harness string `json:"harness"`
	File    string `json:"file"`
	State   string `json:"state"`
	Rule    string `json:"rule,omitempty"`
	Present bool   `json:"-"`
}

// Wired reports whether this checkout's sessions of the harness get satelle's
// hooks: installed, whether or not git sees the file.
func (w Wiring) Wired() bool { return w.State != WiringNotInstalled }

// WiringOf reads the wiring of every harness (Harnesses, in order) in the
// repo at root. It reads the hook files and asks git which installed ones an
// ignore rule hides; where git cannot answer (no repository, no git) none is
// reported hidden. It writes nothing.
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
