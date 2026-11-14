package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// What satelle doctor --resolve did with an issue it acted on.
const (
	resultApplied  = "applied"  // the service action ran
	resultReleased = "released" // the seat was released
	resultKept     = "kept"     // the person kept the seat
	resultOpened   = "opened"   // the finding is on the fix item just opened
	resultCarried  = "carried"  // the finding is on the fix item already open
	resultFailed   = "failed"   // the action failed; detail says why
)

// answerRelease, or its first letter, is the answer at the terminal that
// releases an orphaned seat; any other answer keeps it.
const (
	answerRelease = "release"
)

// configFirst prefixes each finding left because the configuration is invalid:
// no command can record an item until it validates.
const configFirst = "fix the configuration first (satelle validate); then: "

// resolveUnsetFirst prefixes each finding left because doctor.toml [resolve]
// names no fix item kind or tag.
const resolveUnsetFirst = "set [resolve] kind and tag in .satelle/" + kernel.DoctorFile + " (satelle help configuration); then: "

// Why --resolve left an issue (resolveLeft.Why); an ask left for want of a
// terminal says so with the open error.
const (
	leftNoFix  = "doctor cannot prove a fix for it"
	leftConfig = "the configuration is invalid, so nothing can be recorded until it validates"
	leftNoTTY  = "no controlling terminal to ask at"
)

// resolveAction is one action --resolve took on an issue.
type resolveAction struct {
	Check   string `json:"check"`
	Problem string `json:"problem"`
	Resolve string `json:"resolve"`
	Result  string `json:"result"`
	Detail  string `json:"detail,omitempty"`
}

// resolveLeft is an issue --resolve left for the user, with why it was left
// and its fix.
type resolveLeft struct {
	Check   string `json:"check"`
	Problem string `json:"problem"`
	Why     string `json:"why"`
	Fix     string `json:"fix"`
}

// resolveReport is what satelle doctor --resolve did and left; Item is the fix
// item opened or carrying the substrate findings.
type resolveReport struct {
	Actions []resolveAction `json:"actions"`
	Left    []resolveLeft   `json:"left"`
	Item    string          `json:"item,omitempty"`
	carried bool            // Item was already open
}

func (r *resolveReport) act(is doctorIssue, result, detail string) {
	r.Actions = append(r.Actions, resolveAction{Check: is.Check, Problem: is.Problem, Resolve: is.Resolve, Result: result, Detail: detail})
}

func (r *resolveReport) leave(is doctorIssue, prefix, why string) {
	r.Left = append(r.Left, resolveLeft{Check: is.Check, Problem: is.Problem, Why: why, Fix: prefix + is.Fix})
}

// failed reports whether any action failed.
func (r resolveReport) failed() bool {
	for _, a := range r.Actions {
		if a.Result == resultFailed {
			return true
		}
	}
	return false
}

// resolveDoctor acts on what doctor found in in. What it can prove it fixes:
// it installs or restarts the service, releases seats whose session has ended
// on this machine or whose item is in a terminal state, and opens one item
// listing every substrate finding. Every other orphaned seat is asked about on
// the controlling terminal, and without one listed with that reason. Every
// issue it leaves is listed with why. Its only git commands are doctor's read-only queries
// (gitfacts TopLevel and IgnoredBy), and it never edits the repo tree:
// its writes are the user's unit files and [service], ledger events and the
// session records.
func resolveDoctor(ctx context.Context, in *doctorInput) resolveReport {
	doc := runDoctor(ctx, in)
	rep := resolveReport{Actions: []resolveAction{}, Left: []resolveLeft{}}
	by := map[string][]doctorIssue{}
	for _, is := range doc.Issues {
		by[is.Resolve] = append(by[is.Resolve], is)
	}

	// Service: install covers a restart (it restarts satelled too).
	switch {
	case len(by[resolveInstallService]) > 0:
		var buf bytes.Buffer
		err := installService(&buf, 0, "", "")
		for _, is := range append(by[resolveInstallService], by[resolveRestartService]...) {
			rep.act(is, outcome(err), detail(buf.String(), err))
		}
	case len(by[resolveRestartService]) > 0:
		var buf bytes.Buffer
		err := restartUnit(&buf)
		for _, is := range by[resolveRestartService] {
			rep.act(is, outcome(err), detail(buf.String(), err))
		}
	}
	for _, is := range by[""] {
		rep.leave(is, "", leftNoFix)
	}

	// The kernel refuses to open on any configuration problem, so nothing can
	// be recorded: list every ledger action as left, the configuration first.
	ledgerIssues := append(append(append([]doctorIssue{}, by[resolveFixItem]...), by[resolveReleaseSeat]...), by[resolveAsk]...)
	if configBroken(in, doc.Issues) {
		var rest []doctorIssue
		for _, is := range ledgerIssues {
			if is.Check == "config" {
				rep.leave(is, "", leftConfig)
			} else {
				rest = append(rest, is)
			}
		}
		for _, is := range rest {
			rep.leave(is, configFirst, leftConfig)
		}
		return rep
	}
	// No [resolve] kind or tag: there is no item to put the findings on, and
	// satelle guesses none (sto_a5cdb024). List them, naming the key to set;
	// the seats are still resolved.
	fixItems := by[resolveFixItem]
	if unset := resolveUnset(in); len(unset) > 0 && len(fixItems) > 0 {
		why := fmt.Sprintf(".satelle/%s [resolve] %s is not set, so there is no item to put the finding on", kernel.DoctorFile, strings.Join(unset, " and "))
		for _, is := range fixItems {
			rep.leave(is, resolveUnsetFirst, why)
		}
		fixItems = nil
		ledgerIssues = append(append([]doctorIssue{}, by[resolveReleaseSeat]...), by[resolveAsk]...)
	}
	if len(ledgerIssues) == 0 {
		pruneSessions(in)
		return rep
	}
	k, err := in.r.open()
	if err != nil {
		for _, is := range ledgerIssues {
			rep.act(is, resultFailed, err.Error())
		}
		return rep
	}
	defer k.Close()
	resolveSeats(k, &rep, by[resolveReleaseSeat], by[resolveAsk])
	openFixItem(k, in, &rep, fixItems)
	pruneSessions(in)
	return rep
}

// resolveUnset names the doctor.toml [resolve] keys --resolve needs to open
// the fix item that are not set (kernel.DoctorResolve.Unset).
func resolveUnset(in *doctorInput) []string {
	if in.cfg == nil {
		return []string{"kind", "tag"}
	}
	return in.cfg.Doctor.Resolve.Unset()
}

// configBroken reports whether the configuration is invalid, so no ledger
// action can be recorded: it failed to load, or doctor reported a config issue.
func configBroken(in *doctorInput, issues []doctorIssue) bool {
	if in.cfgErr != nil {
		return true
	}
	for _, is := range issues {
		if is.Check == "config" {
			return true
		}
	}
	return false
}

func outcome(err error) string {
	if err != nil {
		return resultFailed
	}
	return resultApplied
}

func detail(out string, err error) string {
	d := strings.TrimSpace(out)
	if err != nil {
		d = strings.TrimSpace(d + "\n" + err.Error())
	}
	return d
}

// resolveSeats releases each proven orphan without asking, then asks about
// each other one on the controlling terminal, opened once and only when there
// is one to ask about. Without a terminal those are left, saying so.
func resolveSeats(k *kernel.Kernel, rep *resolveReport, proven, ask []doctorIssue) {
	for _, is := range proven {
		if err := k.Release(is.item, is.session, actor()); err != nil {
			rep.act(is, resultFailed, err.Error())
			continue
		}
		rep.act(is, resultReleased, "")
	}
	if len(ask) == 0 {
		return
	}
	tty, err := openTerminal()
	if err != nil {
		for _, is := range ask {
			rep.leave(is, "", leftNoTTY+" ("+err.Error()+")")
		}
		return
	}
	defer tty.Close()
	rd := bufio.NewReader(tty)
	for _, is := range ask {
		fmt.Fprintf(tty, "%s: %s\n  fix: %s\nrelease or keep? [keep]: ", is.Check, is.Problem, is.Fix)
		line, _ := rd.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case answerRelease, answerRelease[:1]:
			if err := k.Release(is.item, is.session, actor()); err != nil {
				rep.act(is, resultFailed, err.Error())
				continue
			}
			rep.act(is, resultReleased, "on the answer at the terminal")
		default:
			rep.act(is, resultKept, "on the answer at the terminal")
		}
	}
}

// openFixItem puts every substrate finding on one item, for the agent to fix
// in one change through the item's workflow: the open one [resolve] selects
// when there is one, else a new one. It never edits or commits the files.
func openFixItem(k *kernel.Kernel, in *doctorInput, rep *resolveReport, findings []doctorIssue) {
	if len(findings) == 0 {
		return
	}
	sel := in.cfg.Doctor.Resolve.Selection()
	items, err := k.List(cache.Query{Kind: sel.Kind, Tag: sel.Tag, Category: sel.Category})
	if err != nil {
		for _, is := range findings {
			rep.act(is, resultFailed, err.Error())
		}
		return
	}
	for _, it := range items {
		if w := k.Workflows[it.Workflow]; w != nil && w.IsTerminal(it.Status) {
			continue
		}
		rep.Item, rep.carried = it.ID, true
		for _, is := range findings {
			rep.act(is, resultCarried, it.ID)
		}
		return
	}
	title := fmt.Sprintf("Fix %d substrate finding(s) satelle doctor reported", len(findings))
	var body strings.Builder
	fmt.Fprintf(&body, "`satelle doctor --resolve` found these at %s on %s. Fix them all in this item's change and commit it through the item's workflow; `satelle doctor` lists what remains.\n\n",
		in.r.Tree, time.Now().Format("2006-01-02"))
	criteria := make([]string, 0, len(findings))
	for _, is := range findings {
		fmt.Fprintf(&body, "- %s: %s — fix: %s\n", is.Check, is.Problem, is.Fix)
		criteria = append(criteria, fmt.Sprintf("satelle doctor no longer reports %s: %s", is.Check, is.Problem))
	}
	f := item.Fields{Kind: &sel.Kind, Title: &title, Criteria: &criteria, Tags: &[]string{sel.Tag}}
	b := body.String()
	f.Body = &b
	if sel.Category != "" {
		f.Category = &sel.Category
	}
	it, err := k.Create(f, actor())
	if err != nil {
		for _, is := range findings {
			rep.act(is, resultFailed, err.Error())
		}
		return
	}
	rep.Item = it.ID
	for _, is := range findings {
		rep.act(is, resultOpened, it.ID)
	}
}

// pruneSessions removes the session records of this machine whose session has
// ended and holds no seat any more.
func pruneSessions(in *doctorInput) {
	if !in.inRepo {
		return // another repo's seats cannot be read here
	}
	items, err := readItems(in)
	if err != nil {
		return
	}
	seated := map[string]bool{}
	for _, ri := range items {
		for _, s := range ri.seats {
			seated[s.Session] = true
		}
	}
	_ = seatlife.Prune(config.GlobalDir(), func(s string) bool { return seated[s] })
}

// printResolve is --resolve's text report.
func printResolve(out io.Writer, rep resolveReport) {
	for _, a := range rep.Actions {
		fmt.Fprintf(out, "%s: %s: %s\n", a.Result, a.Check, a.Problem)
		if a.Detail != "" {
			for _, line := range strings.Split(a.Detail, "\n") {
				fmt.Fprintf(out, "  %s\n", line)
			}
		}
	}
	switch {
	case rep.Item == "":
	case rep.carried:
		fmt.Fprintf(out, "substrate findings carried by %s\n", rep.Item)
	default:
		fmt.Fprintf(out, "substrate findings opened as %s\n", rep.Item)
	}
	for _, l := range rep.Left {
		fmt.Fprintf(out, "left: %s: %s\n  why: %s\n  fix: %s\n", l.Check, l.Problem, l.Why, l.Fix)
	}
	if len(rep.Actions) == 0 && len(rep.Left) == 0 {
		fmt.Fprintln(out, "no issues")
	}
}
