package cli

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// stopCheck decides a turn's end for the session the payload names: block,
// with the reason, when an item the session is seated on owes the session's
// own next move (kernel.NextMove) and the session's last stop was not
// blocked (seatlife.Stop). Every other case lets the stop through: a Stop
// that ends no turn (grok's session-end fire), a dispatched run (its session
// comes from SATELLE_SESSION), no session, no satelle repo, a pre-1.0 repo,
// a configuration that does not open.
func stopCheck(in hook.Input) (reason string, block bool) {
	if !in.TurnEnd() || sessionFromEnv() != "" {
		return "", false
	}
	session := in.Session
	if session == "" {
		return "", false
	}
	r, err := findRepo(hookDir(in))
	if err != nil || len(kernel.LegacyMarkers(r.DataDir)) > 0 {
		return "", false
	}
	k, err := r.open()
	if err != nil {
		return "", false
	}
	defer k.Close()
	seats, err := k.Cache.Seats(session, "")
	if err != nil {
		return "", false
	}
	var moves []kernel.Move
	seen := map[string]bool{}
	for _, s := range seats {
		if seen[s.Item] {
			continue
		}
		seen[s.Item] = true
		mv, err := k.NextMove(s.Item, session)
		if err != nil || !mv.Owed() {
			continue
		}
		moves = append(moves, mv)
	}
	if !seatlife.Stop(config.GlobalDir(), session, len(moves) > 0) {
		return "", false
	}
	return stopReason(moves, session, sessionGuidance(r)), true
}

// stopReason is a blocked stop's reason: the session line, one line per
// seated item that owes a move, then the repo's session guidance (the drive
// rule and the promise that the next stop passes are its wording).
func stopReason(moves []kernel.Move, session, guidance string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "satelle session: %s\n", session)
	for _, mv := range moves {
		b.WriteString(moveLine(mv, session))
		b.WriteString("\n")
	}
	b.WriteString(guidance)
	return strings.TrimRight(b.String(), "\n")
}

// moveLine names one seated item's next move as the command that makes it.
func moveLine(mv kernel.Move, s string) string {
	const tail = ", not an external wait"
	head := fmt.Sprintf("%s %s: ", mv.Item, mv.Status)
	fire := func(q string) string { return fmt.Sprintf("satelle queue fire %s --session %s", q, s) }
	switch {
	case mv.Act == kernel.MoveFire:
		return head + "next is " + fire(mv.Target) + tail
	case mv.Queue == "":
		return head + fmt.Sprintf("next is satelle %s set %s --status %s --session %s", mv.TargetKind, mv.Target, mv.To, s) + tail
	}
	var cmd string
	if mv.Act == kernel.MoveSet {
		cmd = fmt.Sprintf("satelle %s set %s --status %s --session %s", mv.TargetKind, mv.Target, mv.To, s)
	} else {
		cmd = fmt.Sprintf("satelle %s seat %s --session %s", mv.TargetKind, mv.Target, s)
	}
	run := fmt.Sprintf("%s (%s)", mv.Target, mv.TargetStatus)
	if mv.Carries {
		holder := "this session holds it"
		if mv.Act == kernel.MoveSeat {
			holder = "no session holds it"
		}
		return head + fmt.Sprintf("run %s carries it and %s: next is %s", run, holder, cmd) + tail
	}
	holder := "this session holds that run"
	if mv.Act == kernel.MoveSeat {
		holder = "no session holds that run"
	}
	return head + fmt.Sprintf("queue %s's last run %s is still open and does not list it; %s: next is %s, then %s once %s is done",
		mv.Queue, run, holder, cmd, fire(mv.Queue), mv.Target) + tail
}
