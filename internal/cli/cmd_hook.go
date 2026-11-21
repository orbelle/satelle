package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
	"github.com/bobmcallan/satelle/internal/syncstate"
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
the session guidance satelle init writes and the repo edits. The session-start
context opens with the description of each [hook] inject principle whose
frontmatter has framing: true. When printing the
[hook] inject files in full would take the session-start context over the
calling harness's limit in .satelle/harness.toml, context lists each file by
description and path instead (see satelle help configuration). Grok reads the session
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

func hookSession(in hook.Input) string {
	if s := sessionFromEnv(); s != "" {
		return s
	}
	return in.Session
}

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

func hookDefers(harness string, in hook.Input) bool {
	return hook.Defers(harness, in, func() (string, bool) {
		r, err := findRepo(hookDir(in))
		return r.Tree, err == nil
	})
}

var tempRootsOverride []string

func runHookGate(cmd *cobra.Command, harness string, in hook.Input) error {
	if !in.IsEdit() && !in.IsShell() {
		return nil
	}
	if hookDefers(harness, in) {
		return nil
	}
	r, err := findRepo(hookDir(in))
	if err != nil {
		return nil
	}
	session := hookSession(in)
	if session != "" {
		_ = seatlife.ObserveMeta(config.GlobalDir(), session, hookMeta(harness, in))
	}
	g := hook.Gate{Root: r.Tree, TempRoots: tempRootsOverride}
	k, err := r.open()
	if err != nil {
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
		switch editScope(k.Workflows, it) {
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

func grantUnconfigured(r repo, session, tree string) hook.Grant {
	g := seatGrantUnconfigured(r, session, tree)
	g.Tree = false
	return g
}

func seatGrantUnconfigured(r repo, session, tree string) hook.Grant {
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
		case scope == workflow.EditsTree:
			g.Tree = true
		}
	}
	return g
}

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
	recordSession(harness, in, seatlife.Start)
	text, notice, ok := sessionContext(in, harness, true)
	if !ok {
		return nil
	}
	return printAnswer(cmd, hook.Context(harness, hook.EventSessionStart, text, notice))
}

func printAnswer(cmd *cobra.Command, a []byte) error {
	if a == nil {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), string(a))
	return err
}

func runHookPrompt(cmd *cobra.Command, harness string, in hook.Input) error {
	if hookDefers(harness, in) {
		return nil
	}
	recordSession(harness, in, seatlife.Resolve)
	text, _, ok := sessionContext(in, harness, false)
	if !ok {
		return nil
	}
	return printAnswer(cmd, hook.Context(harness, hook.EventPrompt, text, ""))
}

func runHookStopcheck(cmd *cobra.Command, harness string, in hook.Input) error {
	if hookDefers(harness, in) {
		return nil
	}
	if reason, block := stopCheck(in); block {
		return printAnswer(cmd, hook.StopBlock(harness, reason))
	}
	return printAnswer(cmd, hook.StopAnswer(harness))
}

func hookMeta(harness string, in hook.Input) seatlife.Meta {
	return seatlife.Meta{Harness: in.Harness(harness), Model: in.Model}
}

func recordSession(harness string, in hook.Input, record func(home, session string, ancestors []procgroup.Proc, meta seatlife.Meta) error) {
	session := hookSession(in)
	if session == "" {
		return
	}
	if _, err := findRepo(hookDir(in)); err != nil {
		return
	}
	anc := procgroup.Ancestors(os.Getpid())
	meta := hookMeta(harness, in)
	if len(anc) == 0 && meta == (seatlife.Meta{}) {
		return
	}
	_ = record(config.GlobalDir(), session, anc, meta)
}

func sessionContext(in hook.Input, harness string, inject bool) (text, notice string, ok bool) {
	r, err := findRepo(hookDir(in))
	if err != nil {
		return "", "", false
	}
	if m := kernel.LegacyMarkers(r.DataDir); len(m) > 0 {
		line := needsUpgradeLine(r.Tree, m) + "\n"
		if inject {
			notice = line
		}
		return line, notice, true
	}
	src := &sessionSource{Session: hookSession(in), Dir: hookDir(in)}
	if k, err := r.open(); err != nil {
		src.Invalid = err
	} else {
		defer k.Close()
		src.Settings, src.Harness = k.Settings, k.Harness
		src.Seats = func(session string) []seatView {
			seats, _ := seatsOf(k, session)
			return seats
		}
	}
	text, notice, _ = renderSession(r, src, harness, inject)
	return text, notice, true
}

func sessionLine(session string) string {
	if session == "" {
		return "satelle session: unknown (the harness sent no session id and SATELLE_SESSION is unset).\n"
	}
	return fmt.Sprintf("satelle session: %s\n", session)
}

func renderSession(r repo, k *sessionSource, harness string, inject bool) (text, notice string, idx *indexedContext) {
	session := k.Session
	var b strings.Builder
	if err := k.Invalid; err != nil {
		fmt.Fprintf(&b, "satelle: this repo's configuration is invalid; run `satelle validate`.\n%v\n", err)
		b.WriteString(sessionLine(session))
		if inject {
			block, n := sessionDoctor(k.Dir, harness)
			b.WriteString(block)
			notice = n
		}
		return b.String(), notice, nil
	}
	if inject {
		b.WriteString(framingLines(r, k.Settings.Hook.Inject))
	}
	b.WriteString(sessionLine(session))
	if session != "" {
		seats := k.Seats(session)
		printSeats(&b, seats)
	}
	if g := sessionGuidance(r); g != "" {
		b.WriteString(g)
		b.WriteString("\n")
	}
	if inject {
		block, n := sessionDoctor(k.Dir, harness)
		b.WriteString(block)
		notice = n
		preface := b.String()
		for _, f := range k.Settings.Hook.Inject {
			body, err := contextFile(r, f)
			if err != nil {
				fmt.Fprintf(&b, "\nsatelle: [hook] inject file %s: %v\n", f, err)
				continue
			}
			fmt.Fprintf(&b, "\n%s\n", body)
		}
		if overLimit(harness, b.String(), notice, k.Harness[harness].ContextLimitBytes, len(k.Settings.Hook.Inject)) {
			entries := injectIndex(r, k.Settings.Hook.Inject)
			return indexForm(preface, entries, r.Tree, harness), notice, &indexedContext{InlineBytes: emittedBytes(harness, b.String(), notice), Limit: k.Harness[harness].ContextLimitBytes, Entries: entries}
		}
	}
	return b.String(), notice, nil
}

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

func sessionGuidance(r repo) string {
	body, err := contextFile(r, filepath.Join(r.DataDir, kernel.SessionFile))
	if err == nil {
		return body
	}
	if errors.Is(err, fs.ErrNotExist) && slices.Contains(kernel.InitPolicy(r.DataDir).Removed, kernel.SessionFile) {
		return ""
	}
	name := filepath.ToSlash(filepath.Join(filepath.Base(r.DataDir), kernel.SessionFile))
	line := fmt.Sprintf("satelle: %s is missing, so this session has no command guidance; adding it is a substrate story for the user to decide to raise (satelle init says how).", name)
	if !errors.Is(err, fs.ErrNotExist) {
		line += fmt.Sprintf(" (%v)", err)
	}
	return line
}

func sessionDoctor(dir, harness string) (block, notice string) {
	ctx, cancel := context.WithTimeout(context.Background(), doctorHookBudget)
	defer cancel()
	in := loadDoctorInput(dir)
	in.harness = harness
	in.sessionStart = true
	rep := runDoctor(ctx, in)
	if in.inRepo {
		if st, _, err := syncstate.Load(config.GlobalDir(), uiRepoRoot(in.r)); err == nil {
			rep = leftForSession(rep, st.Unattended, time.Now())
		}
	}
	return doctorBlock(rep, doctorHookBudget), doctorNotice(rep)
}
