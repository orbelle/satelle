package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/logfile"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

type seatFix func(k *kernel.Kernel, why string, synced func(string) bool) []string

type projectFix struct {
	path  string
	r     repo
	seats resolveReport
}

func allSynced(string) bool { return true }

func syncedIn(res ledgerrepl.SyncResult) func(string) bool {
	if len(res.Push.Refused) > 0 || len(res.Pull.Refused) > 0 {
		return func(string) bool { return false }
	}
	out := map[string]bool{}
	for _, u := range res.Unsynced {
		out[u.Item] = true
	}
	for _, id := range append(res.Push.DivergedItems(), res.Pull.DivergedItems()...) {
		out[id] = true
	}
	return func(id string) bool { return !out[id] }
}

func unattendedSeats(ctx context.Context, in *doctorInput, k *kernel.Kernel, why string, synced func(string) bool) (resolveReport, []string) {
	rep := resolveReport{Actions: []resolveAction{}, Left: []resolveLeft{}}
	if in.cfg == nil {
		return rep, nil
	}
	issues, _ := checkSeats(ctx, in)
	broken := false
	if k == nil {
		cfgIssues, _ := checkConfig(ctx, in)
		broken = configBroken(in, cfgIssues)
	}
	var proven []doctorIssue
	for _, is := range issues {
		switch {
		case is.Resolve == resolveAsk:
			rep.leave(is, "", leftNoTTY)
		case is.Resolve != resolveReleaseSeat:
			rep.leave(is, "", leftNoFix)
		case k == nil && broken:
			rep.leave(is, configFirst, leftConfig)
		case k == nil:
			rep.act(is, resultFailed, why)
		case why != "":
			rep.leave(is, "", leftNotSynced+": "+why)
		case !synced(is.item):
			rep.leave(is, "", leftNotSynced)
		default:
			proven = append(proven, is)
		}
	}
	if len(proven) == 0 {
		return rep, nil
	}
	start := len(rep.Actions)
	resolveSeats(k, &rep, proven, nil, syncServiceName)
	var released []string
	seen := map[string]bool{}
	for i, is := range proven {
		if rep.Actions[start+i].Result == resultReleased && !seen[is.item] {
			seen[is.item] = true
			released = append(released, is.item)
		}
	}
	return rep, released
}

func unattendedMachine(ctx context.Context) resolveReport {
	rep := resolveReport{Actions: []resolveAction{}, Left: []resolveLeft{}}
	issues, _ := checkVersions(ctx, nil)
	var restart []doctorIssue
	for _, is := range issues {
		switch {
		case unattended(is):
			restart = append(restart, is)
		case is.Resolve == resolveRestartService:
			rep.leave(is, "", leftStopped)
		case is.Resolve == resolveInstallService:
			rep.leave(is, "", leftInstall)
		default:
			rep.leave(is, "", leftNoFix)
		}
	}
	if len(restart) == 0 {
		return rep
	}
	var buf bytes.Buffer
	err := restartUnit(&buf)
	for _, is := range restart {
		rep.act(is, outcome(err), detail(buf.String(), err))
	}
	return rep
}

const machineProject = "-"

var logField = strings.NewReplacer("\t", " ", "\r\n", " ", "\n", " ")

func logUnattended(out io.Writer, project string, actions []resolveAction, now time.Time) {
	path := mirror.DoctorLogPath(config.GlobalDir())
	for _, a := range actions {
		fields := []string{now.Format(time.RFC3339), a.Result, project, a.Check + ": " + a.Problem}
		if a.Detail != "" {
			fields = append(fields, a.Detail)
		}
		for i, f := range fields {
			fields[i] = logField.Replace(f)
		}
		if err := logfile.Append(now, path, logfile.DefaultConfig, strings.Join(fields, "\t")); err != nil {
			fmt.Fprintf(out, "doctor log not written: %v\n", err)
			return
		}
	}
}

func reportUnattended(out io.Writer, project string, actions []resolveAction, now time.Time) {
	logUnattended(out, project, actions, now)
	for _, a := range actions {
		fmt.Fprintf(out, "%s: doctor: %s %s: %s\n", project, a.Result, a.Check, a.Problem)
	}
}

func recordUnattended(r repo, seats, machine resolveReport, now time.Time) error {
	u := syncstate.Unattended{At: now}
	for _, rep := range []resolveReport{seats, machine} {
		for _, a := range rep.Actions {
			u.Actions = append(u.Actions, syncstate.UnattendedAction{Check: a.Check, Key: a.key, Problem: a.Problem, Result: a.Result, Detail: a.Detail})
		}
		for _, l := range rep.Left {
			u.Left = append(u.Left, syncstate.UnattendedIssue{Check: l.Check, Key: l.key, Problem: l.Problem, Why: l.Why, Fix: l.Fix})
		}
	}
	return syncstate.RecordUnattended(config.GlobalDir(), uiRepoRoot(r), u)
}

func leftForSession(rep doctorReport, u syncstate.Unattended, now time.Time) doctorReport {
	if u.At.IsZero() || now.Sub(u.At) > mirror.StaleAfter {
		return rep
	}
	kept := make([]doctorIssue, 0, len(rep.Issues))
	for _, is := range rep.Issues {
		if unattended(is) && !u.Names(is.Check, is.key()) {
			continue
		}
		kept = append(kept, is)
	}
	rep.Issues = kept
	return rep
}
