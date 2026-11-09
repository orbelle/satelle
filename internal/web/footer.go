package web

import "github.com/bobmcallan/satelle/internal/buildinfo"

// InstalledBuilds are the identity lines of the satelle CLI installed beside
// satelled and of the satelled binary on disk, as recorded in the satelle home
// (buildinfo.Lookup): satelled never runs a binary to learn them.
type InstalledBuilds struct {
	CLI    string
	Daemon string
}

// installedBuilds reports the installed builds; nil means not reported, so the
// footer shows unknown rows and compares nothing.
var installedBuilds func() InstalledBuilds

// SetInstalledBuilds sets the reader of the installed builds, a process global
// like SetBasePath. satelled calls it before serving.
func SetInstalledBuilds(fn func() InstalledBuilds) { installedBuilds = fn }

// buildsVM is the footer's three build lines, which of them disagree, and one
// warning per disagreement with its remedy (buildinfo.Drift, the rule satelle
// service status uses).
type buildsVM struct {
	CLI       string
	Running   string
	Installed string
	Mark      map[string]bool
	Warnings  []buildinfo.Mismatch
}

// newBuilds fills the footer from the running build and the installed record.
func newBuilds() buildsVM {
	b := buildsVM{Running: buildinfo.Resolve().String(), Mark: map[string]bool{}}
	if installedBuilds != nil {
		ib := installedBuilds()
		b.CLI, b.Installed = ib.CLI, ib.Daemon
	}
	b.Warnings = buildinfo.Drift(b.CLI, b.Running, b.Installed)
	for _, m := range b.Warnings {
		for _, l := range m.Lines {
			b.Mark[l] = true
		}
	}
	return b
}
