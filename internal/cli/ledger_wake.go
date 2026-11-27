package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/bobmcallan/satelle/internal/kernel"
)

func wakeQueues(out io.Writer, path string, k *kernel.Kernel, names []string, synced func(string) bool) []string {
	var written []string
	for _, name := range names {
		queueID, _ := k.ResolveQueue(name)
		if synced == nil || !synced(queueID) {
			fmt.Fprintf(out, "%s: wake %s: not fired: not in sync with the store\n", path, name)
			continue
		}
		f, err := k.Fire(name, "", "wake", syncServiceName)
		switch {
		case errors.Is(err, kernel.ErrRunOpen), errors.Is(err, kernel.ErrQueueMoved):
			fmt.Fprintf(out, "%s: wake %s: held: %v\n", path, name, err)
		case err != nil:
			fmt.Fprintf(out, "%s: wake %s: not fired: %v\n", path, name, err)
		case f.Run == "":
		default:
			fmt.Fprintf(out, "%s: wake %s: fired %s (%s) over %d item(s)\n", path, name, f.Run, f.Status, len(f.Inputs))
			written = append(written, f.Queue, f.Run)
		}
	}
	return written
}
