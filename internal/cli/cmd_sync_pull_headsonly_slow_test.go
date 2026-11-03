//go:build clislow

package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/snapsync"
)

// Whatever `sync status` names as an area's next command must change the area when
// run: a pull is never named for an area it cannot move, and neither is a push.
func TestSlowSyncStatusPullHintIsNeverADeadEnd(t *testing.T) {
	const team = "team skill\n"
	heads := func(t *testing.T) (*snapMachine, *snapFake) {
		m, f := scaffoldOnlyMachine(t)
		f.cfg.put("probe", "skills/team.md", []byte(team))
		return m, f
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) (*snapMachine, *snapFake)
	}{
		{"heads only", heads},
		{"a snapshot behind", func(t *testing.T) (*snapMachine, *snapFake) {
			_, b, f := behindFixture(t)
			return b, f
		}},
		{"heads only, lost claim race", func(t *testing.T) (*snapMachine, *snapFake) {
			m, f := heads(t)
			f.onRecordPut = func() {
				f.forgeRecord(t, "skills", 0, map[string]string{"skills/team.md": snapsync.SHA([]byte(team))})
			}
			return m, f
		}},
		{"void base, local present", func(t *testing.T) (*snapMachine, *snapFake) {
			m, f := scaffoldOnlyMachine(t)
			m.write("skills/mine.md", "mine\n")
			m.voidBase("skills", map[string]string{"skills/mine.md": "mine\n"})
			return m, f
		}},
		{"void base, local empty", func(t *testing.T) (*snapMachine, *snapFake) {
			m, f := heads(t)
			m.voidBase("skills", nil)
			return m, f
		}},
		{"unreadable records", func(t *testing.T) (*snapMachine, *snapFake) {
			m, f := heads(t)
			f.unreadableRecords = true
			return m, f
		}},
	}
	moved := func(before, after []string) bool {
		return strings.Join(before[1:4], "|") != strings.Join(after[1:4], "|")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := c.setup(t)
			out, _ := m.status()
			before := row(t, out, "skills")
			switch before[4] {
			case "satelle sync pull":
				pullOut, err := m.pull()
				afterOut, _ := m.status()
				after := row(t, afterOut, "skills")
				if !moved(before, after) && (err == nil || strings.Contains(pullOut, "skills: pulled what it could, now behind; next: satelle sync pull")) {
					t.Errorf("status named a pull that changed nothing: %v -> %v\n%s", before, after, pullOut)
				}
				if err != nil && strings.Contains(pullOut, "next: satelle sync pull") {
					t.Errorf("a failed pull named another pull:\n%s", pullOut)
				}
			case "satelle sync push":
				if pushOut, err := m.tryPushConfig(); err != nil {
					t.Fatalf("the named push failed: %v\n%s", err, pushOut)
				}
				afterOut, _ := m.status()
				if after := row(t, afterOut, "skills"); !moved(before, after) {
					t.Errorf("status named a push that changed nothing: %v -> %v", before, after)
				}
			default:
				t.Fatalf("skills next = %q, want a pull or a push to exercise", before[4])
			}
		})
	}
}
