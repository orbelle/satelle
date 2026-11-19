package web

import (
	"html"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

func fourSeatHome(t *testing.T, here, elsewhere, gone, unverified string) ([]mirror.Seat, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	machine, err := config.MachineID(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := seatlife.Own(home, "s-here", procgroup.Self()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, start := procgroup.ProbeIdentity(cmd.Process.Pid)
	dead := procgroup.Proc{PID: cmd.Process.Pid, Start: start}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := seatlife.Own(home, "s-gone", dead); err != nil {
		t.Fatal(err)
	}
	seat := func(id, session, m, host string) mirror.Seat {
		return mirror.Seat{ID: id + "/" + session, Item: id, Session: session, Location: "local:/repos/x", Machine: m, Host: host}
	}
	return []mirror.Seat{
		seat(here, "s-here", machine, "this-host"),
		seat(elsewhere, "s-elsewhere", "m-other", "other-host"),
		seat(gone, "s-gone", machine, "this-host"),
		seat(unverified, "s-unverified", "", ""),
	}, home
}

func markTitle(t *testing.T, cell, text string) string {
	t.Helper()
	at := strings.Index(cell, ">"+text+"<")
	if at < 0 {
		t.Errorf("no %q mark in %q", text, cell)
		return ""
	}
	span := strings.LastIndex(cell[:at], "<span")
	if span < 0 {
		t.Errorf("the %q mark is not a span: %q", text, cell)
		return ""
	}
	tag := cell[span:at]
	_, rest, ok := strings.Cut(tag, `title="`)
	if !ok {
		t.Errorf("the %q mark has no title: %q", text, tag)
		return ""
	}
	title, _, _ := strings.Cut(rest, `"`)
	return html.UnescapeString(title)
}

var engagedRowRE = regexp.MustCompile(`data-engaged="([^"]*)"[^>]*data-expand-url="fragment/story/([^"]+)"`)

func engagedRows(html string) map[string]string {
	out := map[string]string{}
	for _, m := range engagedRowRE.FindAllStringSubmatch(html, -1) {
		out[m[2]] = m[1]
	}
	return out
}
