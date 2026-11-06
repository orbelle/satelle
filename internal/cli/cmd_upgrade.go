package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/upgrade"
)

func upgradeCmd() *cobra.Command {
	var archive, dryRun bool
	var from, mapPath string
	cmd := &cobra.Command{
		Use:   "upgrade (--archive | --from <old store> --map <status map>)",
		Short: "Move a pre-1.0 repo onto v1: archive its .satelle, then import its old items by a status map",
		Long: `upgrade moves a repo from the pre-1.0 format onto v1, in two steps.

--archive moves the old .satelle, verified file by file, to
<runtime>/archive/<UTC stamp>/.satelle (nothing is deleted) and writes a fresh
v1 .satelle. Port the archived workflows into it (your agent's upgrade step),
then write a status map.

--from <old store> --map <file> imports every old item, open and closed, with
its history and documents, into the v1 ledger. The map decides each item's
workflow and state; an old status it does not name refuses the import before
anything is written. A second run imports nothing twice.

--dry-run prints what either step would do and writes nothing.
See satelle help upgrade.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			switch {
			case archive == (from != ""):
				return errors.New("give exactly one of --archive and --from")
			case from != "" && mapPath == "":
				return errors.New("--from needs --map <status map>")
			}
			r, err := findRepo("")
			if err != nil {
				return err
			}
			if archive {
				return runArchive(cmd, r, dryRun)
			}
			gc, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			if clean, setting := gc.StartCleanFor(filepath.Dir(r.DataDir)); clean {
				return fmt.Errorf("refused: %s is listed in %s; it starts clean and its old ledger is not imported", filepath.Dir(r.DataDir), setting)
			}
			k, err := r.open()
			if err != nil {
				return err
			}
			defer k.Close()
			src, err := upgrade.OpenSource(from)
			if err != nil {
				return err
			}
			m, err := upgrade.LoadMap(mapPath)
			if err != nil {
				return err
			}
			p, err := upgrade.PlanImport(k, src, m)
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Fprint(out, p.Summary(false))
				return nil
			}
			if err := p.Apply(k, actor()); err != nil {
				return err
			}
			fmt.Fprint(out, p.Summary(true))
			return nil
		},
	}
	cmd.Flags().BoolVar(&archive, "archive", false, "archive the pre-1.0 .satelle and write a fresh v1 one")
	cmd.Flags().StringVar(&from, "from", "", "the pre-1.0 store to import (the directory holding satelle.db)")
	cmd.Flags().StringVar(&mapPath, "map", "", "the status map (TOML [status] table: old status -> {workflow, state})")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be done and write nothing")
	return cmd
}

// runArchive archives the old .satelle and initialises a fresh one; it never
// opens the kernel, whose old configuration cannot load.
func runArchive(cmd *cobra.Command, r repo, dryRun bool) error {
	out := cmd.OutOrStdout()
	a, err := upgrade.Archive(r.DataDir, r.Runtime, time.Now(), dryRun)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(out, "would archive %s (%d files; pre-1.0 markers: %v) to %s, then write a fresh v1 .satelle\n", a.Source, a.Files, a.Markers, a.Dest)
		return nil
	}
	if err := kernel.Init(r.DataDir); err != nil {
		return fmt.Errorf("archived to %s, but the fresh init failed: %w", a.Dest, err)
	}
	if probs := kernel.Validate(r.DataDir); len(probs) > 0 {
		return fmt.Errorf("archived to %s, but the fresh .satelle does not validate: %v", a.Dest, errors.Join(probs...))
	}
	fmt.Fprintf(out, "archived %s (%d files) to %s\n", a.Source, a.Files, a.Dest)
	fmt.Fprintf(out, "initialised %s (configuration valid)\n", r.DataDir)
	for _, db := range []string{filepath.Join(r.Runtime, "satelle.db"), filepath.Join(a.Dest, "satelle.db")} {
		if _, err := os.Stat(db); err == nil {
			fmt.Fprintf(out, "old store: %s\n", filepath.Dir(db))
		}
	}
	fmt.Fprintln(out, "next: reinstall harness hooks with `satelle agents install <harness>`; have your agent port the archived workflows and write a status map; then run `satelle upgrade --from <old store> --map <file> --dry-run` (see `satelle help upgrade`)")
	return nil
}
