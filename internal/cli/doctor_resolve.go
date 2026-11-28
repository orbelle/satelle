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
	"github.com/bobmcallan/satelle/internal/syncstate"
)

const (
	resultApplied  = "applied"
	resultRemoved  = "removed"
	resultReleased = syncstate.Released
	resultKept     = "kept"
	resultOpened   = "opened"
	resultCarried  = "carried"
	resultFailed   = "failed"
)

const (
	answerRelease = "release"
)

const configFirst = "fix the configuration first (satelle validate); then: "

const resolveUnsetFirst = "set [resolve] kind and tag in .satelle/" + kernel.DoctorFile + " (satelle help configuration); then: "

const (
	leftNoFix  = "doctor cannot prove a fix for it"
	leftConfig = "the configuration is invalid, so nothing can be recorded until it validates"
	leftNoTTY  = "no controlling terminal to ask at"

	leftNotSynced = "its chain was not proven in sync with the ledger store this pass, so a release could fork it"
	leftInstall   = "installing or updating satelle is not done unattended"
	leftStopped   = "satelled is not answering; only a build skew is restarted unattended"
	leftAgent     = "installing or signing in an agent on this machine is done by hand"
)

type resolveAction struct {
	Check   string `json:"check"`
	Problem string `json:"problem"`
	Resolve string `json:"resolve"`
	Result  string `json:"result"`
	Detail  string `json:"detail,omitempty"`
	key     string
}

type resolveLeft struct {
	Check   string `json:"check"`
	Problem string `json:"problem"`
	Why     string `json:"why"`
	Fix     string `json:"fix"`
	key     string
}

type resolveReport struct {
	Actions []resolveAction `json:"actions"`
	Left    []resolveLeft   `json:"left"`
	Item    string          `json:"item,omitempty"`
	carried bool
}

func (r *resolveReport) act(is doctorIssue, result, detail string) {
	r.Actions = append(r.Actions, resolveAction{Check: is.Check, Problem: is.Problem, Resolve: is.Resolve, Result: result, Detail: detail, key: is.key()})
}

func (r *resolveReport) leave(is doctorIssue, prefix, why string) {
	r.Left = append(r.Left, resolveLeft{Check: is.Check, Problem: is.Problem, Why: why, Fix: prefix + is.Fix, key: is.key()})
}

func (r resolveReport) failed() bool {
	for _, a := range r.Actions {
		if a.Result == resultFailed {
			return true
		}
	}
	return false
}

func resolveDoctor(ctx context.Context, in *doctorInput) resolveReport {
	doc := runDoctor(ctx, in)
	rep := resolveReport{Actions: []resolveAction{}, Left: []resolveLeft{}}
	by := map[string][]doctorIssue{}
	for _, is := range doc.Issues {
		by[is.Resolve] = append(by[is.Resolve], is)
	}

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
	resolveProjects(&rep, by[resolveRemoveProject])
	for _, is := range by[""] {
		rep.leave(is, "", leftNoFix)
	}
	for _, is := range by[resolveAgentSetup] {
		rep.leave(is, "", leftAgent)
	}
	for _, is := range by[resolveUserDecision] {
		rep.leave(is, "", leftUserDecision)
	}

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
	resolveSeats(k, &rep, by[resolveReleaseSeat], by[resolveAsk], actor())
	openFixItem(k, in, &rep, fixItems)
	pruneSessions(in)
	return rep
}

func resolveUnset(in *doctorInput) []string {
	if in.cfg == nil {
		return []string{"kind", "tag"}
	}
	return in.cfg.Doctor.Resolve.Unset()
}

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

func resolveSeats(k *kernel.Kernel, rep *resolveReport, proven, ask []doctorIssue, actor string) {
	for _, is := range proven {
		if err := k.Release(is.item, is.session, actor); err != nil {
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
			if err := k.Release(is.item, is.session, actor); err != nil {
				rep.act(is, resultFailed, err.Error())
				continue
			}
			rep.act(is, resultReleased, "on the answer at the terminal")
		default:
			rep.act(is, resultKept, "on the answer at the terminal")
		}
	}
}

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
		criteria = append(criteria, findingCriterion(is.Check, is.Problem))
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

func pruneSessions(in *doctorInput) {
	if !in.inRepo {
		return
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
