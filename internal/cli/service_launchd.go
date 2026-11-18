package cli

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

const (
	daemonLabel = "dev.satelle.satelled"
	syncLabel   = "dev.satelle.sync"
)

func launchAgentPath(label string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func renderDaemonPlist(daemon, addr string, port int) string {
	return renderPlist(daemonLabel, []string{daemon, "--addr", addr, "--port", strconv.Itoa(port)},
		"\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>KeepAlive</key>\n\t<true/>\n")
}

func renderSyncPlist(cli string) string {
	return renderPlist(syncLabel, []string{cli, "ledger", "autosync"},
		fmt.Sprintf("\t<key>StartInterval</key>\n\t<integer>%d</integer>\n\t<key>RunAtLoad</key>\n\t<true/>\n", int(mirror.SyncPeriod/time.Second)))
}

func renderPlist(label string, args []string, tail string) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + plistText(label) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range args {
		b.WriteString("\t\t<string>" + plistText(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString(tail)
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func plistText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func plistProgram(body string) string {
	d := xml.NewDecoder(strings.NewReader(body))
	afterKey, inArray := false, false
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case inArray && start.Name.Local == "string":
			var s string
			if d.DecodeElement(&s, &start) != nil {
				return ""
			}
			return s
		case inArray:
			return ""
		case start.Name.Local == "key":
			var k string
			if d.DecodeElement(&k, &start) != nil {
				return ""
			}
			afterKey = k == "ProgramArguments"
		case afterKey && start.Name.Local == "array":
			inArray = true
		default:
			afterKey = false
		}
	}
}
