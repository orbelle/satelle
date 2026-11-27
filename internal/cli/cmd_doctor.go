package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func doctorCmd() *cobra.Command {
	var asJSON, resolve bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report what is wrong here, each with the command that fixes it; --resolve fixes what it can prove",
		Long: `doctor reports what is wrong with this repo and this machine's satelle, each
issue with the command that fixes it. Without --resolve it changes nothing: no
file, ledger, runtime or git state is written, and it never contacts the
ledger store.

Its checks:
  config        the configuration is invalid (satelle validate)
  unknown-keys  a key satelle does not read in a lenient .satelle file
                (doctor.toml, ui.toml, init.toml, volatile.toml, harness.toml,
                skills/<name>/pack.toml) or in a known advisory table of
                satelle.toml ([hook], [items], [service], [sync], [ledger]):
                listed with its file, table and value without stopping satelle
                from loading (ask what it is for, then remove it if mistyped or
                keep it if intentional)
  substrate     a satelle command, flag, help topic or [[link]] the repo's
             .satelle skills, principles, tasks, workflows or queues name that
             this binary lacks (satelle validate)
  volatile   a line of a file .satelle/volatile.toml names under paths
             (shipped: the .satelle Markdown) that matches one of its
             volatile patterns (a version string, "currently", a model id),
             unless it matches its dated expression (shipped: a YYYY-MM-DD
             date) or carries the exempt marker; files its exclude globs
             match are not read, nor is a fenced code block in Markdown
             (reword, date or mark it; a file an [[upstream]] entry owns
             names its owner and is fixed there)
  init       files this release ships that the repo lacks (a substrate story
             the user decides to raise: the fix line prints the satelle init
             --print command and the satelle story create command, in the
             category .satelle/init.toml names; a repo that names none is
             told the key and the categories its workflow rules select, and
             given no command)
  agents     each active override in .satelle/agents.local.toml is a note,
             naming the agent and each field as tracked -> effective; the
             local file not covered by an ignore rule, or tracked by git, is
             an issue (add agents.local.toml to .satelle/.gitignore, or
             git rm --cached it, through the repo's workflow)
  repos      with a .satelle/repos.toml or .satelle/repos.local.toml present
             (none: no finding and no git call): each registered repository
             with no checkout mapped here, a mapped checkout that does not
             exist, has no origin remote, or whose origin names another
             repository than repos.toml (both URLs named, never with
             credentials, query or fragment); a checkout that another control
             repository of this machine ([projects] paths) also maps, or that
             is either one's own root, naming both; more repositories than
             .satelle/doctor.toml [repos] max_count; and repos.local.toml not
             covered by an ignore rule or tracked by git. Its only git command
             is a read of each existing checkout's origin URL; it never runs
             a repository's fetch command (satelle repo add, satelle repo list)
  harness    a harness in use (agents.toml runs it, or the repo has its
             .claude, .cursor, .grok or .pi directory) whose hook file differs
             from satelle's current hooks: absent, holding none, running a
             satelle command this binary lacks, or an older set; one git tracks
             is committed in the repo. Of the others, the calling session's
             unwired harness is an issue and the rest a note, and with the
             caller unknown, nothing wired and nothing in use is the issue; a
             hook file an ignore rule hides from git is a note
             (satelle agents install <harness>)
  context    a harness in use whose session-start context, as the index of
             its [hook] inject files, is over its limit in .satelle/harness.toml:
             the issue names the limit and the index size; a harness in use
             whose context with every file inline is over the limit and whose
             index fits is a note naming the limit and the inline size; a
             harness in use with no limit is a note giving its size as
             unbudgeted. It is what satelle hook context would write for that
             harness and the calling session (raise the limit, or shorten what
             the index carries)
  versions   the installed satelle CLI, the running satelled and the installed
             satelled disagree (satelle service restart, satelle update)
  upgrade    this repo's .satelle is pre-1.0 (satelle upgrade --archive, run
             here); each other registered pre-1.0 project is a note
  projects   a project of this machine (~/.satelle/config.toml [projects])
             whose path no longer exists (satelle project remove <path>)
  sync       the sync timer is missing or not active, or the last automatic
             sync pass failed; the last pass's pushed, pulled and forked items
             are a note (read from the recorded pass, never the store)
  seats      a seat whose session has ended on this machine, on an item in a
             terminal state, or at a worktree of this machine that no longer
             exists (satelle <kind> release <id> --session <session>); a note
             classes every seat as here (this machine, session running),
             elsewhere (another machine), gone or unverified
  check:<s>  each check skill .satelle/doctor.toml names: exit 0 prints each
             non-empty line as a note and raises no issue; exit 1 prints each
             line as an issue with the check's fix; any other exit reports
             could not check
  open:<n>   each open item a .satelle/doctor.toml [[items]] entry selects

Outside a satelle repo only the machine checks (versions, upgrade, projects,
sync) run; in a pre-1.0 repo only versions, upgrade, projects and the sync
timer are checked. The
session-start hook runs the same checks, except context, which measures that
hook's own output, within ` + doctorHookBudget.String() + ` and prints a short
summary; a check still running then is listed as not checked. doctor exits 0
when it finds nothing and 1 when it finds an issue.

--resolve fixes what doctor can prove and asks about the rest:
  - a missing satelle.service or sync timer, or a timer not active (on macOS
    a missing or unloaded dev.satelle.satelled or dev.satelle.sync launch
    agent): it runs the satelle service install action; satelled not answering or running
    another build than the installed one: satelle service restart;
  - a project of this machine whose path no longer exists: it removes it from
    ~/.satelle/config.toml [projects], without asking;
  - a seat bound on this machine whose session's process has exited, or a
    seat on an item in a terminal state wherever its session ran: it
    releases it, without asking;
  - every other orphaned seat: it shows the evidence and asks on the
    controlling terminal, release or keep (keep is the default); with no
    terminal it lists it, saying there was no terminal to ask at;
  - a file this release ships that the repo lacks: it lists it with the
    command that raises the story for it, the user's decision, and opens no
    item for it;
  - config, substrate, volatile, agents and .satelle/doctor.toml check findings: it
    opens one item listing them all, for the agent to fix in one change
    through the item's workflow (the kind and tag .satelle/doctor.toml
    [resolve] names, which satelle init writes; with either unset it lists
    the findings and names the key to set), and opens no second one while
    that is open. With an invalid configuration no item can be recorded: it
    lists them, satelle validate first.
It never edits or commits files, and the only git commands doctor runs, with
or without --resolve, are read-only queries: the repo's top level, and
git check-ignore for the harness and agents checks, and git ls-files for the
agents check. Everything else is listed with its
fix, and each issue left with a why: line saying why it was left. Plain
doctor's "acts on N" line counts what --resolve would act on from this
terminal: the seats it would ask about only when a terminal is attached, and
no ledger action under an invalid configuration. --resolve exits 0 when
nothing is left and no action failed, else 1.

The sync timer's pass (satelle-sync.timer) makes the fixes that need no
answer in every project on its own, with no doctor run: it releases a seat
whose session has ended on this machine or whose item is in a terminal state,
only where the ledger store is local or the item was proven in sync with the
store that pass (otherwise it leaves the seat, recording why), and restarts
satelled when it runs another build than the installed one. It never restarts
a satelled that is not answering and never installs or updates satelle. Each
fix it makes is one line of satelle logs --doctor; session start then lists a
seat release or build-skew restart only when the timer's last pass left it.
satelle doctor itself lists every issue.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			in, err := loadDoctorInput("")
			if err != nil {
				return err
			}
			if resolve {
				rep := resolveDoctor(cmd.Context(), in)
				if asJSON {
					b, err := json.MarshalIndent(rep, "", "  ")
					if err != nil {
						return err
					}
					fmt.Fprintln(out, string(b))
				} else {
					printResolve(out, rep)
				}
				if len(rep.Left) > 0 || rep.failed() {
					return &exitError{code: ExitError, msg: fmt.Sprintf("%d issue(s) left", len(rep.Left)), quiet: true}
				}
				return nil
			}
			rep := runDoctor(cmd.Context(), in)
			if asJSON {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(out, string(b))
			} else {
				for _, is := range rep.Issues {
					fmt.Fprintf(out, "%s: %s\n  fix: %s\n", is.Check, is.Problem, is.Fix)
				}
				for _, n := range rep.Notes {
					fmt.Fprintf(out, "note: %s\n", n)
				}
				for _, u := range rep.Unchecked {
					fmt.Fprintf(out, "not checked: %s\n", u)
				}
				if len(rep.Issues) == 0 {
					fmt.Fprintln(out, "no issues")
				} else {
					fmt.Fprintf(out, "%d issue(s)\n", len(rep.Issues))
				}
				if n := resolvable(in, rep.Issues, terminalAttached()); n > 0 {
					fmt.Fprintf(out, "satelle doctor --resolve acts on %d of them\n", n)
				}
			}
			if len(rep.Issues) > 0 {
				return &exitError{code: ExitError, msg: fmt.Sprintf("%d issue(s)", len(rep.Issues)), quiet: true}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	cmd.Flags().BoolVar(&resolve, "resolve", false, "fix what doctor can prove, ask about orphaned seats, open one item for the substrate findings")
	return cmd
}

func resolvable(in *doctorInput, issues []doctorIssue, tty bool) int {
	broken := configBroken(in, issues)
	n := 0
	for _, is := range issues {
		switch is.Resolve {
		case resolveInstallService, resolveRestartService, resolveRemoveProject:
			n++
		case resolveFixItem:
			if !broken && len(resolveUnset(in)) == 0 {
				n++
			}
		case resolveReleaseSeat:
			if !broken {
				n++
			}
		case resolveAsk:
			if !broken && tty {
				n++
			}
		}
	}
	return n
}
