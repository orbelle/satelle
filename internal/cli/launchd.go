package cli

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bobmcallan/satelle/internal/mirror"
)

const (
	launchDaemonLabel = "dev.satelle.satelled"
	launchSyncLabel   = "dev.satelle.sync"
)

type launchRunner func(args ...string) (string, error)

type launchSpec struct {
	Daemon, Addr string
	Port         int
	CLI          string
}

const plistHeader = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`

const plistFooter = `</dict>
</plist>
`

func plistString(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func plistArgs(args ...string) string {
	var b strings.Builder
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range args {
		b.WriteString("\t\t<string>" + plistString(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	return b.String()
}

func renderDaemonPlist(daemon, addr string, port int) string {
	return plistHeader +
		"\t<key>Label</key>\n\t<string>" + launchDaemonLabel + "</string>\n" +
		plistArgs(daemon, "--addr", addr, "--port", strconv.Itoa(port)) +
		"\t<key>RunAtLoad</key>\n\t<true/>\n" +
		"\t<key>KeepAlive</key>\n\t<true/>\n" +
		plistFooter
}

func renderSyncPlist(cli string) string {
	return plistHeader +
		"\t<key>Label</key>\n\t<string>" + launchSyncLabel + "</string>\n" +
		plistArgs(cli, "ledger", "autosync") +
		"\t<key>RunAtLoad</key>\n\t<true/>\n" +
		"\t<key>StartInterval</key>\n\t<integer>" + strconv.Itoa(int(mirror.SyncPeriod.Seconds())) + "</integer>\n" +
		plistFooter
}

type plistNode struct {
	XMLName xml.Name
	Text    string   `xml:",chardata"`
	Strings []string `xml:"string"`
}

type plistDoc struct {
	Dict struct {
		Nodes []plistNode `xml:",any"`
	} `xml:"dict"`
}

func plistProgram(body string) string {
	var doc plistDoc
	if xml.Unmarshal([]byte(body), &doc) != nil {
		return ""
	}
	nodes := doc.Dict.Nodes
	for i := 0; i+1 < len(nodes); i++ {
		if nodes[i].XMLName.Local == "key" && nodes[i].Text == "ProgramArguments" {
			if next := nodes[i+1]; next.XMLName.Local == "array" && len(next.Strings) > 0 {
				return next.Strings[0]
			}
			return ""
		}
	}
	return ""
}

func launchPlistPath(dir, label string) string {
	return filepath.Join(dir, label+".plist")
}

func bootoutNotLoaded(out string) bool {
	for _, s := range []string{"No such process", "Could not find specified service", "not loaded"} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

func bootout(run launchRunner, target string) error {
	out, err := run("bootout", target)
	if err == nil || bootoutNotLoaded(out) {
		return nil
	}
	return fmt.Errorf("launchctl bootout %s: %v: %s", target, err, out)
}

func installLaunchAgents(w io.Writer, run launchRunner, dir string, uid int, s launchSpec) error {
	domain := fmt.Sprintf("gui/%d", uid)
	type agent struct{ label, body string }
	var agents []agent
	if s.Daemon != "" {
		agents = append(agents, agent{launchDaemonLabel, renderDaemonPlist(s.Daemon, s.Addr, s.Port)})
	}
	agents = append(agents, agent{launchSyncLabel, renderSyncPlist(s.CLI)})
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, a := range agents {
		path := launchPlistPath(dir, a.label)
		if err := os.WriteFile(path, []byte(a.body), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote %s\n", path)
		target := domain + "/" + a.label
		if err := bootout(run, target); err != nil {
			return err
		}
		for _, args := range [][]string{{"enable", target}, {"bootstrap", domain, path}} {
			if out, err := run(args...); err != nil {
				return fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, out)
			}
		}
	}
	if s.Daemon != "" {
		fmt.Fprintf(w, "%s loaded in %s\n", launchDaemonLabel, domain)
	}
	fmt.Fprintf(w, "%s loaded in %s: each project on this machine syncs every %s\n", launchSyncLabel, domain, mirror.SyncPeriod)
	return nil
}

func uninstallLaunchAgents(w io.Writer, run launchRunner, dir string, uid int) error {
	domain := fmt.Sprintf("gui/%d", uid)
	labels := []string{launchSyncLabel, launchDaemonLabel}
	var errs []error
	for _, l := range labels {
		if err := bootout(run, domain+"/"+l); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for _, l := range labels {
		path := launchPlistPath(dir, l)
		err := os.Remove(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s\n", path)
	}
	return nil
}
