package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

func hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Harness hook handlers (called by the hook files `satelle agents install` writes)",
		Long: `Each handler reads the harness's hook payload on stdin and answers in the
harness's own envelope. gate is the edit gate (PreToolUse); context prints the
session's context at session start; prompt reminds the session of its seats on
each prompt; stopcheck answers the Stop event. Under cursor, a call Cursor
sends to another harness's entries stays silent once .cursor/hooks.json holds
satelle's own (see satelle help edit-gate).`,
		Hidden: false,
	}
	var harness string
	cmd.PersistentFlags().StringVar(&harness, "harness", hook.Claude, "the calling harness ("+strings.Join(hook.Harnesses, ", ")+")")
	for _, h := range []struct {
		verb, short string
		run         func(cmd *cobra.Command, harness string, in hook.Input) error
	}{
		{hook.VerbGate, "Judge an edit or shell command (PreToolUse)", runHookGate},
		{hook.VerbContext, "Print the session's context (SessionStart)", runHookContext},
		{hook.VerbPrompt, "Remind the session of its seats (UserPromptSubmit)", runHookPrompt},
		{hook.VerbStopcheck, "Answer the Stop event", runHookStopcheck},
	} {
		cmd.AddCommand(&cobra.Command{
			Use:   h.verb,
			Short: h.short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				raw, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				return h.run(cmd, harness, hook.ParseInput(raw))
			},
		})
	}
	return cmd
}

// hookSession is the session a hook acts for: SATELLE_SESSION (a performer
// Perform dispatches carries the session seated for its run), else the harness
// payload's.
func hookSession(in hook.Input) string {
	if s := sessionFromEnv(); s != "" {
		return s
	}
	return in.Session
}

// hookDir is the directory the session works in: satelle's pinned project dir
// (SATELLE_PROJECT_DIR), else the harness's (hook.ProjectDir: claude's, then
// cursor's), else the payload's cwd, else its first workspace root (cursor),
// else the process's.
func hookDir(in hook.Input) string {
	if v := strings.TrimSpace(os.Getenv(config.ProjectDirEnv)); v != "" {
		return v
	}
	if v := hook.ProjectDir(); v != "" {
		return v
	}
	if in.Cwd != "" {
		return in.Cwd
	}
	for _, r := range in.Roots {
		if r = strings.TrimSpace(r); r != "" {
			return r
		}
	}
	wd, _ := os.Getwd()
	return wd
}

// hookDefers reports whether this call is Cursor running another harness's
// satelle entries in a repo where Cursor's own are installed (hook.Defers):
// the handler then answers nothing, so no event is judged or injected twice.
func hookDefers(harness string, in hook.Input) bool {
	if !in.Cursor || harness == hook.Cursor {
		return false
	}
	r, err := findRepo(hookDir(in))
	return err == nil && hook.Defers(r.Tree, harness, in)
}

// hookTempRoots are the gate's temp roots: nil is hook.TempRoots(). A test
// whose repositories live under the temp dir narrows it to reach the fence.
var hookTempRoots []string

func runHookGate(cmd *cobra.Command, harness string, in hook.Input) error {
	if !in.IsEdit() && !in.IsShell() {
		return nil
	}
	if hookDefers(harness, in) {
		return nil
	}
	r, err := findRepo(hookDir(in))
	if err != nil {
		return nil // not a satelle repo: nothing to gate
	}
	session := hookSession(in)
	g := hook.Gate{Root: r.Tree, TempRoots: hookTempRoots}
	k, err := r.open()
	if err != nil {
		// A broken configuration refuses every edit but its own repair.
		rel, _ := filepath.Rel(r.Tree, r.DataDir)
		g.Exempt = []string{filepath.ToSlash(rel) + "/**"}
		g.Seated = func(string) bool { return false }
		v := judgeHook(g, in)
		if !v.Allow {
			v.Reason = "satelle: this repo's configuration is invalid, so only its own files may be edited until `satelle validate` passes.\n" + err.Error()
		}
		return writeVerdict(cmd, harness, v)
	}
	defer k.Close()
	g.Exempt = k.Settings.Gate.EditExemptPaths
	g.Locked = k.Settings.Gate.LockedPaths
	g.Seated = func(tree string) bool { return seated(k, session, tree) }
	v := judgeHook(g, in)
	if !v.Allow && session == "" {
		v.Reason += " (no session id: the harness sent none and SATELLE_SESSION is unset)"
	}
	return writeVerdict(cmd, harness, v)
}

func judgeHook(g hook.Gate, in hook.Input) hook.Verdict {
	cwd := in.Cwd
	if cwd == "" {
		cwd = g.Root
	}
	cwd = canonicalPath(cwd)
	if in.IsShell() {
		return g.Bash(in.Command, cwd)
	}
	p := in.Path
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return g.Edit(canonicalPath(p))
}

func writeVerdict(cmd *cobra.Command, harness string, v hook.Verdict) error {
	if v.Allow {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), string(hook.Deny(harness, v.Reason)))
	return err
}

// seated reports whether session holds a seat at tree on an item whose
// current state has a performer.
func seated(k *kernel.Kernel, session, tree string) bool {
	if session == "" {
		return false
	}
	seats, err := k.Cache.Seats(session, "")
	if err != nil {
		return false
	}
	want := kernel.LocalLocation(tree)
	for _, s := range seats {
		if s.Location != want {
			continue
		}
		if it, ok, err := k.Cache.Item(s.Item); err == nil && ok && performing(k, it) {
			return true
		}
	}
	return false
}

// canonicalPath resolves symlinks in the longest existing prefix of p, so a
// path compares equal to the canonical tree it lies in.
func canonicalPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for dir := p; ; {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

func runHookContext(cmd *cobra.Command, harness string, in hook.Input) error {
	if hookDefers(harness, in) {
		return nil
	}
	recordSession(in, seatlife.Start)
	text, notice, ok := sessionContext(in, harness, true)
	if !ok {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), string(hook.Context(harness, hook.EventSessionStart, text, notice)))
	return err
}

// runHookPrompt settles the session's owner and reminds it of its seats.
// Cursor's beforeSubmitPrompt cannot add context, so under cursor the hook
// only settles the owner and lets the prompt through ({"continue":true}).
func runHookPrompt(cmd *cobra.Command, harness string, in hook.Input) error {
	if hookDefers(harness, in) {
		return nil
	}
	recordSession(in, seatlife.Resolve)
	text, _, ok := sessionContext(in, harness, false)
	if !ok {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), string(hook.Context(harness, hook.EventPrompt, text, "")))
	return err
}

// runHookStopcheck answers the turn's end. satelle adds nothing at a stop, so
// the answer is the harness's empty one: none for claude, pi and grok, an
// empty object for cursor (no followup_message, so Cursor does not continue).
func runHookStopcheck(cmd *cobra.Command, harness string, in hook.Input) error {
	if hookDefers(harness, in) {
		return nil
	}
	a := hook.StopAnswer(harness)
	if a == nil {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), string(a))
	return err
}

// recordSession records this hook's process chain for its session on this
// machine (internal/seatlife): SessionStart starts or updates the record, and
// each hook call settles a missing owner from the process it shares with an
// earlier call; only inside a satelle repo. Errors are ignored and the hook's output is unchanged; a
// session without a record is only ever unverified.
func recordSession(in hook.Input, record func(home, session string, ancestors []procgroup.Proc) error) {
	session := hookSession(in)
	if session == "" {
		return
	}
	if _, err := findRepo(hookDir(in)); err != nil {
		return // not a satelle repo: no seat to stand for
	}
	anc := procgroup.Ancestors(os.Getpid())
	if len(anc) == 0 {
		return
	}
	_ = record(config.GlobalDir(), session, anc)
}

// sessionContext is what the context hook prints: the session id, its seats
// and, at session start, the doctor block (judged for harness, the hook's
// caller) and the [hook] inject files. notice is doctor's short summary for
// the user, empty when inject is false or doctor found no issue. In a pre-1.0
// repo both are the one needs-upgrade line. ok is false outside a repo.
func sessionContext(in hook.Input, harness string, inject bool) (text, notice string, ok bool) {
	r, err := findRepo(hookDir(in))
	if err != nil {
		return "", "", false
	}
	// A pre-1.0 repo is fixed in the repo (sto_0f76dd4e): the agent and the
	// user get the one line naming the upgrade, not the configuration dump.
	if m := kernel.LegacyMarkers(r.DataDir); len(m) > 0 {
		line := needsUpgradeLine(r.Tree, m) + "\n"
		if inject {
			notice = line
		}
		return line, notice, true
	}
	session := hookSession(in)
	var b strings.Builder
	k, err := r.open()
	if err != nil {
		fmt.Fprintf(&b, "satelle: this repo's configuration is invalid; run `satelle validate`.\n%v\n", err)
		if inject {
			block, n := sessionDoctor(hookDir(in), harness)
			b.WriteString(block)
			notice = n
		}
		return b.String(), notice, true
	}
	defer k.Close()
	if session == "" {
		b.WriteString("satelle session: unknown (the harness sent no session id and SATELLE_SESSION is unset).\n")
	} else {
		fmt.Fprintf(&b, "satelle session: %s (pass it as --session, or export SATELLE_SESSION=%s).\n", session, session)
		seats, _ := seatsOf(k, session)
		printSeats(&b, seats)
	}
	b.WriteString("Edits are allowed only while this session holds a seat, at this worktree, on an item whose current state has a performer: `satelle story seat <id> --session <session>`. See `satelle help edit-gate`.\n")
	if inject {
		block, n := sessionDoctor(hookDir(in), harness)
		b.WriteString(block)
		notice = n
	}
	if inject {
		for _, f := range k.Settings.Hook.Inject {
			p := f
			if !filepath.IsAbs(p) {
				p = filepath.Join(r.Tree, f)
			}
			body, err := os.ReadFile(p)
			if err != nil {
				fmt.Fprintf(&b, "\nsatelle: [hook] inject file %s: %v\n", f, err)
				continue
			}
			fmt.Fprintf(&b, "\n%s\n", strings.TrimRight(string(body), "\n"))
		}
	}
	return b.String(), notice, true
}

// sessionDoctor runs satelle doctor's checks within doctorHookBudget for the
// session-start context. It never blocks the session (a check still running
// at the deadline is named as not checked) and never contacts the ledger store.
// It returns the block for the model's context and the notice for the user.
func sessionDoctor(dir, harness string) (block, notice string) {
	ctx, cancel := context.WithTimeout(context.Background(), doctorHookBudget)
	defer cancel()
	in := loadDoctorInput(dir)
	in.harness = harness
	rep := runDoctor(ctx, in)
	return doctorBlock(rep, doctorHookBudget), doctorNotice(rep)
}
