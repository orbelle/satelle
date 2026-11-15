package web

import "github.com/bobmcallan/satelle/internal/buildinfo"

type InstalledBuilds struct {
	CLI    string
	Daemon string
}

var installedBuilds func() InstalledBuilds

func SetInstalledBuilds(fn func() InstalledBuilds) { installedBuilds = fn }

type buildsVM struct {
	CLI       string
	Running   string
	Installed string
	Mark      map[string]bool
	Warnings  []buildinfo.Mismatch
}

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
