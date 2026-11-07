package item

import (
	"regexp"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func ptr[T any](v T) *T { return &v }

func TestFold(t *testing.T) {
	s, _ := ledger.Open(t.TempDir())
	s.Append("itm_1", KindCreated, "me", Fields{Kind: ptr("note"), Title: ptr("a"), Status: ptr("s0"), Tags: ptr([]string{"x"}), Workflow: ptr("w")})
	s.Append("itm_1", "free.kind", "me", map[string]int{"n": 1})
	s.Append("itm_1", KindUpdated, "me", Fields{Title: ptr("b"), Status: ptr("ignored"), Criteria: ptr([]string{"c1"})})
	s.Append("itm_1", KindTransitioned, "me", Move{From: "s0", To: "s1"})
	events, _ := s.Read("itm_1")
	it, err := Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "itm_1" || it.Title != "b" || it.Status != "s1" || it.Kind != "note" || len(it.Criteria) != 1 || it.Tags[0] != "x" || it.Workflow != "w" {
		t.Fatalf("fold: %+v", it)
	}
	if !it.UpdatedAt.Equal(events[3].At) || !it.CreatedAt.Equal(events[0].At) {
		t.Fatal("timestamps")
	}
	if PriorStatus(events) != "s0" {
		t.Fatal("prior status")
	}
	if _, err := Fold(events[1:]); err == nil {
		t.Fatal("chain without create accepted")
	}
}

func TestSeatsAndAttachments(t *testing.T) {
	s, _ := ledger.Open(t.TempDir())
	s.Append("i", KindCreated, "me", Fields{Title: ptr("t")})
	s.Append("i", KindSeatBound, "me", Seat{Session: "s1", Location: "local:/a", Base: "abc"})
	s.Append("i", KindSeatBound, "me", Seat{Session: "s2", Location: "cloud:x"})
	s.Append("i", KindSeatReleased, "me", SeatRelease{Session: "s1"})
	s.Append("i", KindAttachmentAdd, "me", Attachment{Name: "plan.md", SHA256: "ff", Size: 3, MediaType: "text/markdown"})
	events, _ := s.Read("i")
	seats := FoldSeats(events)
	if len(seats) != 1 || seats[0].Session != "s2" || seats[0].Item != "i" {
		t.Fatalf("seats %+v", seats)
	}
	atts := FoldAttachments(events)
	if len(atts) != 1 || atts[0].Name != "plan.md" || atts[0].Actor != "me" {
		t.Fatalf("attachments %+v", atts)
	}
}

func TestNewIDAndSelection(t *testing.T) {
	if !regexp.MustCompile(`^abc_[0-9a-f]{8}$`).MatchString(NewID("abc")) {
		t.Fatal("id shape")
	}
	it := Item{Kind: "k", Status: "s", Tags: []string{"t"}, Category: "c"}
	if !(Selection{Kind: "k", Tag: "t"}).Matches(it) || (Selection{Status: "x"}).Matches(it) {
		t.Fatal("selection")
	}
}

// The zero trigger clears an item's trigger; any other sets it.
func TestFieldsApplyTrigger(t *testing.T) {
	it := Item{Trigger: &Trigger{Task: "tas_1", MinSize: 2}}
	Fields{Trigger: &Trigger{Workflow: "w"}}.Apply(&it)
	if it.Trigger == nil || it.Trigger.Workflow != "w" || it.Trigger.MinSize != 0 {
		t.Fatalf("set: %+v", it.Trigger)
	}
	Fields{Trigger: &Trigger{}}.Apply(&it)
	if it.Trigger != nil {
		t.Fatalf("the zero trigger did not clear: %+v", it.Trigger)
	}
}
