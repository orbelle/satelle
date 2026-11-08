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

func TestTypedMediaType(t *testing.T) {
	for _, c := range []struct{ mt, typ, want string }{
		{"text/markdown", "plan", "text/markdown; type=plan"},
		{"text/markdown; charset=utf-8", "deploy-record", "text/markdown; charset=utf-8; type=deploy-record"},
		{"text/plain; type=other", "plan", "text/plain; type=plan"},
		{"not a media type;;", "plan", ""},
	} {
		if got := TypedMediaType(c.mt, c.typ); got != c.want {
			t.Errorf("TypedMediaType(%q, %q) = %q, want %q", c.mt, c.typ, got, c.want)
		}
	}
	if got := MediaTypeParam("text/markdown; type=deploy-record", "type"); got != "deploy-record" {
		t.Errorf("MediaTypeParam = %q", got)
	}
	if got := MediaTypeParam("text/markdown", "type"); got != "" {
		t.Errorf("MediaTypeParam without the parameter = %q", got)
	}
}
