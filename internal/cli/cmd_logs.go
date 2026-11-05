package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/logsread"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func logsCmd() *cobra.Command {
	var tail int
	var showPath, server bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Read this repo's runtime logs, or satelled's (--server)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var path string
			if server {
				path = mirror.ServerLogPath(config.GlobalDir())
			} else {
				r, err := findRepo("")
				if err != nil {
					return err
				}
				dir := filepath.Join(r.Runtime, "logs")
				if showPath {
					fmt.Fprintln(cmd.OutOrStdout(), dir)
					return nil
				}
				f, ok, err := logsread.Select(dir, "", "")
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				if !ok {
					return fmt.Errorf("no logs under %s", dir)
				}
				path = f.Path
			}
			if showPath {
				fmt.Fprintln(cmd.OutOrStdout(), path)
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "# %s\n%s\n", path, strings.Join(logsread.LastNLines(string(raw), tail), "\n"))
			return nil
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 50, "last N lines")
	cmd.Flags().BoolVar(&showPath, "path", false, "print the log path instead")
	cmd.Flags().BoolVar(&server, "server", false, "satelled's log")
	return cmd
}
