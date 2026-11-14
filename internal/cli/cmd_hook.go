package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Harness hook handlers (called by the hook files `satelle agents install` writes)",
		Long: `Each handler reads the harness's hook payload on stdin and answers in the
harness's own envelope. gate is the edit gate (PreToolUse); context prints the
session's context at session start; prompt reminds the session of its seats on
each prompt; stopcheck answers the Stop event, blocking a turn's end once
when a seated item owes the session's own next move. The context, the
prompt and a blocked stop's reason carry the repo's .satelle/session.md,
the session guidance satelle init writes and the repo edits. Grok reads the session
context as its SessionStart systemMessage and discards a prompt hook's output,
so the prompt hook prints nothing for grok. Under cursor, a call Cursor
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
		g.Grant = func(tree string) hook.Grant { return grantUnconfigured(r, session, tree) }
		v := judgeHook(g, in)
		switch {
		case v.Allow:
		case v.AttachOnly:
			v.Reason += " This repo's configuration is also invalid; repair it from a session that holds no seat at this tree, or in the operator's own editor (`satelle validate --session <session>` lists every problem).\n" + err.Error()
		default:
			v.Reason = "satelle: this repo's configuration is invalid, so only its own files may be edited until `satelle validate` passes.\n" + err.Error()
		}
		return writeVerdict(cmd, harness, v)
	}
	defer k.Close()
	g.Exempt = k.Settings.Gate.EditExemptPaths
	g.Locked = k.Settings.Gate.LockedPaths
	g.Grant = func(tree string) hook.Grant { return grantAt(k, session, tree) }
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

// grantAt is what session's seats at tree allow: a seat on a state whose
// performer may change the tree grants the tree; the first seat on an
// attachments-only state marks the grant attachments-only, naming its item
// and state.
func grantAt(k *kernel.Kernel, session, tree string) hook.Grant {
	var g hook.Grant
	if session == "" {
		return g
	}
	seats, err := k.Cache.Seats(session, "")
	if err != nil {
		return g
	}
	want := kernel.LocalLocation(tree)
	for _, s := range seats {
		if s.Location != want {
			continue
		}
		it, ok, err := k.Cache.Item(s.Item)
		if err != nil || !ok {
			continue
		}
		switch editScope(k, it) {
		case workflow.EditsTree:
			g.Tree = true
		case workflow.EditsAttachments:
			if !g.AttachOnly {
				g.AttachOnly, g.Item, g.State = true, it.ID, it.Status
			}
		}
	}
	return g
}

// grantUnconfigured is grantAt while the repo's configuration does not load:
// the session's seats come from the ledger and cache, opened without it, and
// each seat's edit scope from a lenient read of its workflow file
// (workflow.StateEditScope). A scope that cannot be read, or seats that
// cannot be read, count as attachments-only. Tree is never granted here: a
// broken configuration refuses every edit but its own repair.
func grantUnconfigured(r repo, session, tree string) hook.Grant {
	var g hook.Grant
	if session == "" {
		return g
	}
	_, ch, err := openRecord(r)
	if err != nil {
		return hook.Grant{AttachOnly: true, Unread: "this session's seats cannot be read: " + err.Error()}
	}
	defer ch.Close()
	seats, err := ch.Seats(session, "")
	if err != nil {
		return hook.Grant{AttachOnly: true, Unread: "this session's seats cannot be read: " + err.Error()}
	}
	want := kernel.LocalLocation(tree)
	dir := filepath.Join(r.DataDir, kernel.WorkflowsDir)
	for _, s := range seats {
		if s.Location != want || g.AttachOnly {
			continue
		}
		it, ok, err := ch.Item(s.Item)
		if err != nil {
			g = hook.Grant{AttachOnly: true, Unread: "this session's seats cannot be read: " + err.Error()}
			continue
		}
		if !ok {
			continue
		}
		scope, err := workflow.StateEditScope(dir, it.Workflow, it.Status)
		switch {
		case err != nil:
			g = hook.Grant{AttachOnly: true, Item: it.ID, State: it.Status, Unread: err.Error()}
		case scope == workflow.EditsAttachments:
			g = hook.Grant{AttachOnly: true, Item: it.ID, State: it.Status}
		}
	}
	return g
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
	return printAnswer(cmd, hook.Context(harness, hook.EventSessionStart, text, notice))
}

// printAnswer prints a hook's answer; a nil answer prints nothing.
func printAnswer(cmd *cobra.Command, a []byte) error {
	if a == nil {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), string(a))
	return err
}

// runHookPrompt settles the session's owner and reminds it of its seats.
// Cursor's beforeSubmitPrompt cannot add context, so under cursor the hook
// only settles the owner and lets the prompt through ({"continue":true});
// grok discards an allowing prompt hook's output, so under grok it prints
// nothing.
func runHookPrompt(cmd *cobra.Command, harness string, in hook.Input) error {
	if hookDefers(harness, in) {
		return nil
	}
	recordSession(in, seatlife.Resolve)
	text, _, ok := sessionContext(in, harness, false)
	if !ok {
		return nil
	}
	return printAnswer(cmd, hook.Context(harness, hook.EventPrompt, text, ""))
}

// runHookStopcheck answers the turn's end (stopCheck): it blocks once, in
// the harness's envelope, when an item this session is seated on owes the
// session's own next move, and otherwise answers the harness's empty stop
// answer: none for claude, pi and grok, an empty object for cursor (no
// followup_message, so Cursor does not continue).
func runHookStopcheck(cmd *cobra.Command, harness string, in hook.Input) error {
	if hookDefers(harness, in) {
		return nil
	}
	if reason, block := stopCheck(in); block {
		return printAnswer(cmd, hook.StopBlock(harness, reason))
	}
	return printAnswer(cmd, hook.StopAnswer(harness))
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

// sessionContext is what the context hook prints: the session id, its seats,
// the repo's session guidance (.satelle/session.md, see sessionGuidance) and,
// at session start, the doctor block (judged for harness, the hook's caller)
// and the [hook] inject files. notice is doctor's short summary for the user,
// empty when inject is false or doctor found no issue. In a pre-1.0 repo both
// are the one needs-upgrade line. ok is false outside a repo.
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
		fmt.Fprintf(&b, "satelle session: %s\n", session)
		seats, _ := seatsOf(k, session)
		printSeats(&b, seats)
	}
	if g := sessionGuidance(r); g != "" {
		b.WriteString(g)
		b.WriteString("\n")
	}
	if inject {
		block, n := sessionDoctor(hookDir(in), harness)
		b.WriteString(block)
		notice = n
		for _, f := range k.Settings.Hook.Inject {
			body, err := contextFile(r, f)
			if err != nil {
				fmt.Fprintf(&b, "\nsatelle: [hook] inject file %s: %v\n", f, err)
				continue
			}
			fmt.Fprintf(&b, "\n%s\n", body)
		}
	}
	return b.String(), notice, true
}

// contextFile reads a repo file for the hook context: path is taken against
// the repo tree unless absolute, and the body loses its trailing newlines. It
// is the one reader for the session guidance and the [hook] inject files.
func contextFile(r repo, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Tree, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(body), "\n"), nil
}

// sessionGuidance is the repo's own session guidance, .satelle/session.md
// (kernel.SessionFile), printed by the context and prompt hooks and in a
// blocked stop's reason: the command guidance and the drive rule are
// substrate the repo owns, read on every call. A missing or unreadable file
// is named in one line; the binary never prints its embedded copy.
func sessionGuidance(r repo) string {
	body, err := contextFile(r, filepath.Join(r.DataDir, kernel.SessionFile))
	if err == nil {
		return body
	}
	name := filepath.ToSlash(filepath.Join(filepath.Base(r.DataDir), kernel.SessionFile))
	line := fmt.Sprintf("satelle: %s is missing, so this session has no command guidance; satelle init writes it.", name)
	if !errors.Is(err, fs.ErrNotExist) {
		line += fmt.Sprintf(" (%v)", err)
	}
	return line
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
