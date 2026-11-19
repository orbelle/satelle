package workflow

import "testing"

func TestIsFraming(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want bool
	}{
		"a framing principle":       {"---\ntype: principle\nframing: true\ndescription: d\n---\n\nbody\n", true},
		"a quoted marker":           {"---\ntype: principle\nframing: \"true\"\n---\n", true},
		"a skill with the marker":   {"---\ntype: skill\nframing: true\n---\n\nbody\n", false},
		"another value":             {"---\ntype: principle\nframing: yes\n---\n", false},
		"a principle with no key":   {"---\ntype: principle\n---\n", false},
		"the marker with no type":   {"---\nframing: true\n---\n", false},
		"no frontmatter":            {"type: principle\nframing: true\n", false},
		"the marker below the body": {"---\ntype: principle\n---\n\nframing: true\n", false},
	} {
		sk, err := ParseSkill("f", tc.text)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := sk.IsFraming(); got != tc.want {
			t.Errorf("%s: IsFraming %v, want %v", name, got, tc.want)
		}
	}
}
