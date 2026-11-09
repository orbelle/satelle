package item

import (
	"regexp"
	"slices"
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

func TestFoldedAndSinceEntered(t *testing.T) {
	for _, k := range []string{KindCreated, KindUpdated, KindTransitioned, KindSeatBound, KindSeatReleased, KindAttachmentAdd} {
		if !Folded(k) {
			t.Errorf("%s is read by the folds", k)
		}
	}
	for _, k := range []string{"github.review", "gate.run", "performer.run", ""} {
		if Folded(k) {
			t.Errorf("%s is carried, not folded", k)
		}
	}
	events := []ledger.Event{{Kind: KindCreated}, {Kind: KindAttachmentAdd}, {Kind: KindTransitioned}, {Kind: KindAttachmentAdd, ID: "after"}}
	if got := SinceEntered(events); len(got) != 1 || got[0].ID != "after" {
		t.Errorf("SinceEntered = %+v", got)
	}
	if got := SinceEntered(events[:2]); len(got) != 2 {
		t.Errorf("never moved: SinceEntered = %+v", got)
	}
}

// Explicit assignees are a list on item.created and item.updated; a chain's
// legacy assignee string folds as a one-entry list (sto_600b9a85 AC2).
func TestFoldAssignees(t *testing.T) {
	fold := func(t *testing.T, payloads ...Fields) []string {
		t.Helper()
		s, _ := ledger.Open(t.TempDir())
		for i, f := range payloads {
			kind := KindUpdated
			if i == 0 {
				kind = KindCreated
			}
			s.Append("i", kind, "me", f)
		}
		events, _ := s.Read("i")
		it, err := Fold(events)
		if err != nil {
			t.Fatal(err)
		}
		return it.Assignees
	}
	cases := []struct {
		name     string
		payloads []Fields
		want     []string
	}{
		{"created", []Fields{{Title: ptr("t"), Assignees: ptr([]string{"a", "b"})}}, []string{"a", "b"}},
		{"updated", []Fields{{Title: ptr("t"), Assignees: ptr([]string{"a", "b"})}, {Assignees: ptr([]string{"c"})}}, []string{"c"}},
		{"legacy", []Fields{{Title: ptr("t"), Assignee: ptr("usr_1")}}, []string{"usr_1"}},
		{"legacy empty clears", []Fields{{Title: ptr("t"), Assignees: ptr([]string{"a"})}, {Assignee: ptr("")}}, nil},
		{"list wins", []Fields{{Title: ptr("t"), Assignee: ptr("usr_1"), Assignees: ptr([]string{"a"})}}, []string{"a"}},
		{"untouched", []Fields{{Title: ptr("t"), Assignees: ptr([]string{"a"})}, {Title: ptr("u")}}, []string{"a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fold(t, c.payloads...); !slices.Equal(got, c.want) {
				t.Fatalf("assignees %q, want %q", got, c.want)
			}
		})
	}
}

// AssigneesOf lists explicit users then seated ones, each once, marking the
// seated ones working (sto_600b9a85 AC3).
func TestAssigneesOf(t *testing.T) {
	cases := []struct {
		name           string
		assigned       []string
		seats          []Seat
		users, working []string
	}{
		{"none", nil, nil, []string{}, []string{}},
		{"explicit only", []string{"ann"}, nil, []string{"ann"}, []string{}},
		{"seated in bind order", nil, []Seat{{User: "bob"}, {User: "ada"}}, []string{"bob", "ada"}, []string{"bob", "ada"}},
		{"explicit then seated", []string{"ann"}, []Seat{{User: "bob"}}, []string{"ann", "bob"}, []string{"bob"}},
		{"listed and seated once", []string{"ann", "bob"}, []Seat{{User: "bob"}, {User: "bob"}}, []string{"ann", "bob"}, []string{"bob"}},
		{"empty user skipped", nil, []Seat{{User: ""}, {User: "cy"}}, []string{"cy"}, []string{"cy"}},
		{"explicit dupes", []string{"ann", "ann"}, nil, []string{"ann"}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			users, working := AssigneesOf(c.assigned, c.seats)
			if users == nil || working == nil {
				t.Fatalf("nil result: %v %v", users, working)
			}
			if !slices.Equal(users, c.users) || !slices.Equal(working, c.working) {
				t.Fatalf("got %q working %q, want %q working %q", users, working, c.users, c.working)
			}
		})
	}
}
