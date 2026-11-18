//go:build darwin

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

const (
	daemonDefName  = "the satelled launch agent (" + daemonLabel + ")"
	syncDefName    = "the sync launch agent (" + syncLabel + ")"
	defNoun        = "launch agent"
	serviceManaged = true
)

var bootoutWait = 5 * time.Second

func launchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func agentTarget(label string) string { return domain() + "/" + label }

func launchctlStep(args ...string) error {
	if out, err := launchctl(args...); err != nil {
		return fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

func agentLoaded(label string) bool {
	_, err := launchctl("print", agentTarget(label))
	return err == nil
}

func applyService(w io.Writer, s serviceSpec, force bool) error {
	type agent struct{ label, body string }
	var agents []agent
	if s.Daemon != "" {
		agents = append(agents, agent{daemonLabel, renderDaemonPlist(s.Daemon, s.Addr, s.Port)})
	}
	agents = append(agents, agent{syncLabel, renderSyncPlist(s.CLI)})
	written := map[string]bool{}
	for _, a := range agents {
		wrote, err := writeDefinition(w, launchAgentPath(a.label), a.body, force, plistProgram)
		if err != nil {
			return err
		}
		written[a.label] = wrote
	}
	_, daemonErr := os.Stat(launchAgentPath(daemonLabel))
	for _, label := range []string{daemonLabel, syncLabel} {
		path := launchAgentPath(label)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		loaded := agentLoaded(label)
		switch {
		case written[label] || !loaded:
			if err := launchctlStep("enable", agentTarget(label)); err != nil {
				return err
			}
			if loaded {
				if err := unload(label); err != nil {
					return err
				}
			}
			if err := launchctlStep("bootstrap", domain(), path); err != nil {
				return err
			}
		case label == daemonLabel:
			if err := launchctlStep("kickstart", "-k", agentTarget(label)); err != nil {
				return err
			}
		}
	}
	if daemonErr == nil {
		fmt.Fprintf(w, "%s loaded and (re)started\n", daemonLabel)
	}
	fmt.Fprintf(w, "%s loaded: each project on this machine syncs every %s\n", syncLabel, mirror.SyncPeriod)
	return nil
}

func unload(label string) error {
	bout, berr := launchctl("bootout", agentTarget(label))
	deadline := time.Now().Add(bootoutWait)
	for {
		if !agentLoaded(label) {
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	msg := fmt.Sprintf("launchctl bootout %s: still loaded after %s", agentTarget(label), bootoutWait)
	if berr != nil {
		msg += fmt.Sprintf("; bootout: %v: %s", berr, bout)
	}
	return errors.New(msg)
}

func uninstallUnit(w io.Writer) error {
	labels := []string{syncLabel, daemonLabel}
	for _, label := range labels {
		if !agentLoaded(label) {
			continue
		}
		if err := unload(label); err != nil {
			return err
		}
	}
	for _, label := range labels {
		path := launchAgentPath(label)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintf(w, "removed %s\n", path)
	}
	return nil
}

func restartUnit(w io.Writer) error {
	if err := launchctlStep("kickstart", "-k", agentTarget(daemonLabel)); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s restarted\n", daemonLabel)
	return nil
}

func agentState(label string) string {
	if _, err := os.Stat(launchAgentPath(label)); err != nil {
		return "not installed"
	}
	out, err := launchctl("print", agentTarget(label))
	if err != nil {
		return "not loaded"
	}
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "state = "); ok {
			return strings.TrimSpace(v)
		}
	}
	return "loaded"
}

func printManagerStatus(w io.Writer) {
	fmt.Fprintf(w, "agent     %s (%s)\n", launchAgentPath(daemonLabel), agentState(daemonLabel))
	fmt.Fprintf(w, "sync      %s (%s)\n", launchAgentPath(syncLabel), agentState(syncLabel))
}

func daemonDefPath() string { return launchAgentPath(daemonLabel) }

func defDaemon(body string) string { return plistProgram(body) }

func syncDefPath() string { return launchAgentPath(syncLabel) }

func syncTimerState() string { return agentState(syncLabel) }

func syncTimerLive(state string) bool { return state != "not installed" && state != "not loaded" }
