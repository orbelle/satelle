package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hostapp/herdr"
)

const herdrForceFix = "satelle herdr install --force"

func herdrCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "herdr",
		Short: "Install satelle's stories panel into herdr-app",
		Long: `herdr installs satelle's stories panel and its 'satelle' layout into herdr-app
(herdr.dev), the desktop app for terminals and coding agents. The panel is
read-only: it shows the focused folder's stories, their criteria and their
workflow progress from 'satelle story list', 'satelle story route' and
'satelle ledger list'.

The files are mods/satelle/main.js, mods/satelle/mod.json, layouts/satelle.json
and layouts/satelle.svg under herdr's home: ~/.config/herdr-app on Linux, macOS
and Windows, or $` + herdr.EnvHome + `. The machine setting herdr.home
('satelle settings set herdr.home <folder>') points satelle elsewhere, over both.

'satelle update' (through 'satelle agents refresh') refreshes the panel where
it was installed, and never adds it to a machine that did not install it.`,
		Args: cobra.NoArgs,
	}
	var force bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Write satelle's panel and layout into herdr-app's home",
		Long: `install writes satelle's stories panel and layout into herdr-app's home and
names each file it writes. A file changed outside satelle, or one satelle never
wrote under a name it uses, is left and named; --force replaces it. When the
home came from $` + herdr.EnvHome + ` and is not herdr's default, install records it as the
machine setting herdr.home, so a later refresh finds it without the variable.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			dir, src := herdr.Home(g.Herdr.Home)
			res, err := herdr.Install(dir, config.GlobalDir(), force)
			if errors.Is(err, herdr.ErrNoHome) {
				return &exitError{code: ExitError, msg: fmt.Sprintf("herdr's home %s does not exist (from %s); start herdr-app once, or point satelle at it with 'satelle settings set herdr.home <folder>'", dir, src)}
			}
			for _, p := range res.Written {
				fmt.Fprintf(out, "wrote %s\n", p)
			}
			for _, p := range res.Current {
				fmt.Fprintf(out, "current %s\n", p)
			}
			printHerdrKept(out, res.Kept)
			if err != nil {
				return err
			}
			if src == herdr.SourceEnv && filepath.Clean(dir) != filepath.Clean(herdr.LocalDefault()) {
				g.Herdr.Home = dir
				if err := config.SaveGlobal(g); err != nil {
					return err
				}
				fmt.Fprintf(out, "recorded herdr.home = %q in %s\n", dir, config.GlobalConfigPath())
			}
			return nil
		},
	}
	install.Flags().BoolVar(&force, "force", false, "replace panel files changed outside satelle")
	cmd.AddCommand(install)
	return cmd
}

func printHerdrKept(out io.Writer, kept []herdr.Kept) {
	for _, k := range kept {
		fmt.Fprintf(out, "left %s: %s; '%s' replaces it\n", k.Path, k.Reason, herdrForceFix)
	}
}

func refreshHerdr(out io.Writer) {
	g, err := config.LoadGlobal()
	if err != nil {
		fmt.Fprintf(out, "herdr panel: not refreshed: %v\n", err)
		return
	}
	dir, _ := herdr.Home(g.Herdr.Home)
	res, err := herdr.Refresh(dir, config.GlobalDir())
	switch {
	case res.NotInstalled:
		return
	case res.HomeGone:
		fmt.Fprintf(out, "herdr panel: %s is gone; not refreshed\n", dir)
		return
	}
	for _, p := range res.Written {
		fmt.Fprintf(out, "refreshed herdr panel: %s\n", p)
	}
	printHerdrKept(out, res.Kept)
	if err != nil {
		fmt.Fprintf(out, "herdr panel: not refreshed: %v\n", err)
	}
}

func refreshMachine(out io.Writer) (failed int) {
	failed = refreshAllHooks(out)
	refreshHerdr(out)
	return failed
}
