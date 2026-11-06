package item

import (
	"reflect"
	"testing"
)

// LatestAttachments keeps the newest version of each name, newest first, and
// counts every version, without changing its input.
func TestLatestAttachments(t *testing.T) {
	in := []Attachment{
		{Name: "a", SHA256: "a1"}, {Name: "b", SHA256: "b1"}, {Name: "a", SHA256: "a2"}, {Name: "a", SHA256: "a3"},
	}
	orig := append([]Attachment(nil), in...)
	latest, versions := LatestAttachments(in)
	if want := []Attachment{{Name: "a", SHA256: "a3"}, {Name: "b", SHA256: "b1"}}; !reflect.DeepEqual(latest, want) {
		t.Errorf("latest = %+v, want %+v", latest, want)
	}
	if want := map[string]int{"a": 3, "b": 1}; !reflect.DeepEqual(versions, want) {
		t.Errorf("versions = %v, want %v", versions, want)
	}
	if !reflect.DeepEqual(in, orig) {
		t.Error("the input was changed")
	}
}
