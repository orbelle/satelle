package doctor

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

// checkSyncHealth reports recorded workstate-push state. It never contacts
// satelle.dev and never re-attempts a push (sty_30696eeb).
func checkSyncHealth(repoRoot, dataDir, globalDir string, now time.Time) health.Findings {
	cfgPath := filepath.Join(dataDir, config.ConfigName)
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		return health.Findings{health.Warn(health.IDSyncConfig, "Workstate sync config",
			"could not load satelle.toml; sync health not judged").About(cfgPath)}
	}
	return checkWorkstatePush(cfg, cfgPath, repoRoot, globalDir, now)
}

// checkWorkstatePush judges the recorded work-state push against cfg, under the
// scope the last sync resolved for the repo's explicit binding.
func checkWorkstatePush(cfg config.Config, cfgPath, repoRoot, globalDir string, now time.Time) health.Findings {
	if syncstate.RecordedScope(globalDir, repoRoot, cfg.SyncProject()) == config.LocalScope {
		return health.Findings{health.Info(health.IDSyncLocal, "Work state is local-only",
			"sync scope local ("+localScopeWhy(cfg, globalDir, repoRoot)+"); not flagged as unbacked").About(repoRoot)}
	}
	// A machine that turned push off is not failing to push: the recorded push
	// state is not judged while the switch holds.
	if off, setting := config.MachinePushOffIn(globalDir); off {
		return health.Findings{health.Info(health.IDSyncPushOff, "Hosted push is off",
			fmt.Sprintf("%s; pulls and the local web UI still run, push failures and staleness are not judged", setting)).About(repoRoot)}
	}

	st, _, lerr := syncstate.Load(globalDir, repoRoot)
	if lerr != nil {
		return health.Findings{health.Warn(health.IDSyncConfig, "Workstate sync state",
			lerr.Error()).About(repoRoot)}
	}

	var out health.Findings
	if st.PushReason != "" {
		fix := health.RechainRemediation + ", then `satelle sync workstate push`"
		if health.IsStampRefusal(st.PushReason) {
			// Retrying the push meets the same drift gate; only init re-stamps past it.
			fix = "run `satelle init` to heal the stamp, then `satelle sync push`"
		}
		out = append(out, health.Error(health.IDSyncFailing, "Workstate push failing",
			st.PushReason).About(repoRoot).WithRemediation(fix))
	}

	threshold, has, terr := config.WorkstateStaleAfter(cfg)
	if terr != nil {
		out = append(out, health.Error(health.IDSyncConfig, "Workstate sync config", terr.Error()).About(cfgPath))
		return out
	}
	if !has {
		out = append(out, health.Warn(health.IDSyncConfig, "Workstate staleness not judged",
			"no [sync] stale_after; run `satelle init` or `satelle migrate --yes` to seed it").About(cfgPath))
		if !st.PushLastSuccess.IsZero() {
			out = append(out, health.Info(health.IDSyncOk, "Workstate push",
				fmt.Sprintf("last successful push %s", st.PushLastSuccess.UTC().Format(time.RFC3339))).About(repoRoot))
		}
		return out
	}

	if st.PushLastSuccess.IsZero() {
		out = append(out, health.Error(health.IDSyncUnbacked, "Workstate unbacked",
			fmt.Sprintf("%s: last successful push never (threshold %s)", repoRoot, threshold)).
			About(repoRoot).WithRemediation("run `satelle sync workstate push`"))
		return out
	}
	age := now.Sub(st.PushLastSuccess)
	if age > threshold {
		out = append(out, health.Error(health.IDSyncUnbacked, "Workstate unbacked",
			fmt.Sprintf("%s: last successful push %s ago (threshold %s)", repoRoot, formatAge(age), threshold)).
			About(repoRoot).WithRemediation("run `satelle sync workstate push`"))
		return out
	}
	out = append(out, health.Info(health.IDSyncOk, "Workstate push",
		fmt.Sprintf("last successful push %s", st.PushLastSuccess.UTC().Format(time.RFC3339))).About(repoRoot))
	return out
}

// localScopeWhy says why the offline checks judge a repo local: it is unbound,
// the last sync resolved local for its binding, or no sync has resolved its
// current binding yet.
func localScopeWhy(cfg config.Config, globalDir, repoRoot string) string {
	project := cfg.SyncProject()
	if project == "" {
		return "unbound"
	}
	if st, ok, err := syncstate.Load(globalDir, repoRoot); err == nil && ok && st.Binding == project {
		return "resolved for project " + project + " at " + st.ScopeAt.UTC().Format(time.RFC3339)
	}
	return "not yet resolved for project " + project + " — run `satelle sync status`"
}

func formatAge(d time.Duration) string {
	if d < time.Hour {
		if d < time.Minute {
			return d.Round(time.Second).String()
		}
		return d.Round(time.Minute).String()
	}
	h := int(d.Hours())
	if h < 48 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dd", h/24)
}
