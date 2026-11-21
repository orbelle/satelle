package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/pack"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func packCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pack",
		Short: "Fetch packs from the satelle.dev marketplace",
	}
	cmd.AddCommand(packFetchCmd())
	return cmd
}

func packFetchCmd() *cobra.Command {
	var version, dir, server string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "fetch <ref> --version V --dir D [--server URL]",
		Short: "Fetch a verified version of a pack from the marketplace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := strings.TrimSpace(args[0])
			if ref == "" {
				return errors.New("pack ref is required")
			}
			ver := strings.TrimSpace(version)
			if ver == "" {
				return errors.New("--version is required")
			}
			target := strings.TrimSpace(dir)
			if target == "" {
				return errors.New("--dir is required")
			}
			srv, err := projectServer(server)
			if err != nil {
				return err
			}
			c, err := dial(srv)
			if err != nil {
				return err
			}
			defer c.Close()

			bundle, err := c.Packs.FetchPack(cmd.Context(), &satellepb.GetPackRequest{
				Ref:     ref,
				Version: ver,
			})
			if err != nil {
				if hosted.IsNotFound(err) {
					return fmt.Errorf("pack %s version %s was not found", ref, ver)
				}
				return hosted.Describe("pack fetch", err)
			}

			verified, err := pack.Verify(bundle)
			if err != nil {
				return err
			}
			if verified.Version() != ver {
				return fmt.Errorf("pack %s returned version %q, want %q", ref, verified.Version(), ver)
			}
			if err := pack.Write(target, verified); err != nil {
				return err
			}

			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"ref":     ref,
					"name":    verified.Name(),
					"version": verified.Version(),
					"digest":  verified.Digest(),
					"dir":     target,
					"files":   verified.Paths(),
				})
			}
			digestShort := verified.Digest()
			if len(digestShort) > 12 {
				digestShort = digestShort[:12]
			}
			fmt.Fprintf(cmd.OutOrStdout(), "fetched %s@%s (%d file(s), digest %s) into %s\n", ref, verified.Version(), len(verified.Paths()), digestShort, target)
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "pack version to fetch")
	cmd.Flags().StringVar(&dir, "dir", "", "directory to write verified pack files into")
	cmd.Flags().StringVar(&server, "server", "", "satelle.dev server URL")
	asJSON = jsonFlag(cmd)
	return cmd
}
