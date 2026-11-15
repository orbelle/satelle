package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bobmcallan/satelle/internal/help"
	"github.com/bobmcallan/satelle/internal/substrate"
)

func substrateTree(root *cobra.Command) substrate.Tree {
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	root.InitDefaultVersionFlag()
	t := substrate.NewTree()
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		c.InitDefaultHelpFlag()
		add := func(f *pflag.Flag) { t.AddFlag(path, "--"+f.Name) }
		c.LocalFlags().VisitAll(add)
		c.InheritedFlags().VisitAll(add)
		for _, sub := range c.Commands() {
			if sub.Deprecated != "" {
				continue
			}
			for _, name := range append([]string{sub.Name()}, sub.Aliases...) {
				t.AddSub(path, name)
				walk(sub, strings.TrimSpace(path+" "+name))
			}
		}
	}
	walk(root, "")
	if hc, _, err := root.Find([]string{"help"}); err == nil && hc != root {
		t.HelpPath = hc.Name()
	}
	for _, tp := range help.List() {
		t.Topics[tp.Name] = true
	}
	return t
}
