package substratereview

import (
	"strings"
	"testing"
)

// A team item is applied only when team says this repo holds it; otherwise,
// or with no team check at all, it is refused naming the holder.
func TestRefusalOfATeamItemFollowsItsHolding(t *testing.T) {
	it := ReportItem{Item: Item{Key: "skills/x", Origin: OriginTeam, Source: "/tier/skills/x.md"}}
	held := func(ReportItem) TeamHolding { return TeamHolding{Held: true, Holder: "h@example.com"} }
	notHeld := func(ReportItem) TeamHolding { return TeamHolding{Holder: "h@example.com"} }

	if why := Refusal(it, held); why != "" {
		t.Errorf("a held team item was refused: %s", why)
	}
	for name, team := range map[string]func(ReportItem) TeamHolding{"not held": notHeld, "no check": nil} {
		why := Refusal(it, team)
		if !strings.HasPrefix(why, "skills/x: origin team") {
			t.Errorf("%s: refusal = %q, want an origin team refusal", name, why)
		}
		if team != nil && !strings.Contains(why, "h@example.com") {
			t.Errorf("%s: refusal %q does not name the holder", name, why)
		}
	}
	if why := Refusal(ReportItem{Item: Item{Key: "skills/r", Origin: OriginRepo}}, nil); why != "" {
		t.Errorf("a repo item was refused: %s", why)
	}
}
