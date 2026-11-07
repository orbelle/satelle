package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Harness hook handlers (called by the hook files `satelle agents install` writes)",
		Long: `Each handler reads the harness's hook payload on stdin and answers in the
harness's own envelope. gate is the edit gate (PreToolUse); context prints the
session's context at session start; prompt reminds the session of its seats on
each prompt; stopcheck answers the Stop event.`,
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
		{hook.VerbStopcheck, "Answer the Stop event", func(*cobra.Command, string, hook.Input) error { return nil }},
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

// hookDir is the directory the session works in: the harness's pinned project
// dir, else the payload's cwd, else the process's.
func hookDir(in hook.Input) string {
	for _, k := range []string{config.ProjectDirEnv, "CLAUDE_PROJECT_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if in.Cwd != "" {
		return in.Cwd
	}
	wd, _ := os.Getwd()
	return wd
}

func runHookGate(cmd *cobra.Command, harness string, in hook.Input) error {
	if !in.IsEdit() && !in.IsShell() {
		return nil
	}
	r, err := findRepo(hookDir(in))
	if err != nil {
		return nil // not a satelle repo: nothing to gate
	}
	session := hookSession(in)
	g := hook.Gate{Root: r.Tree}
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

func runHookContext(cmd *cobra.Command, _ string, in hook.Input) error {
	text, ok := sessionContext(in, true)
	if !ok {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), string(hook.Context("SessionStart", text)))
	return err
}

func runHookPrompt(cmd *cobra.Command, _ string, in hook.Input) error {
	text, ok := sessionContext(in, false)
	if !ok {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), string(hook.Context("UserPromptSubmit", text)))
	return err
}

// sessionContext is what the context hook prints: the session id, its seats
// and, at session start, the [hook] inject files. ok is false outside a repo.
func sessionContext(in hook.Input, inject bool) (string, bool) {
	r, err := findRepo(hookDir(in))
	if err != nil {
		return "", false
	}
	session := hookSession(in)
	var b strings.Builder
	k, err := r.open()
	if err != nil {
		fmt.Fprintf(&b, "satelle: this repo's configuration is invalid; run `satelle validate`.\n%v\n", err)
		return b.String(), true
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
	return b.String(), true
}
