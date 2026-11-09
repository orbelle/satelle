package hook

import "testing"

func TestParseAllow(t *testing.T) {
	if w, err := ParseAllow("satelle story set"); err != nil || len(w) != 3 {
		t.Errorf("valid entry: %v %v", w, err)
	}
	for _, e := range []string{"", "  ", "satelle story *", "satelle story set; rm", "satelle $(x)", "satelle 'story'", "satelle story set > f", "a|b"} {
		if _, err := ParseAllow(e); err == nil {
			t.Errorf("%q accepted", e)
		}
	}
}

func TestAllowed(t *testing.T) {
	entries := []string{"satelle story set", "satelle story get"}
	for _, c := range []string{
		"satelle story set sto_1 --status doing",
		"satelle story get sto_1 && satelle story set sto_1 --status doing",
		"satelle story set sto_1 --status=doing",
	} {
		if !Allowed(entries, c) {
			t.Errorf("%q not allowed", c)
		}
	}
	for _, c := range []string{
		"satelle story setx",
		"satelle story record sto_1",
		"satelle status",
		"satelle story set x && rm -rf y",
		"satelle story set $(cat f)",
		"satelle story set `cat f`",
		"satelle story set x > f",
		"satelle story set x 2>&1",
		"satelle story set x &",
		"FOO=1 satelle story set x",
		"bash -c 'satelle story set x'",
		"echo x | xargs satelle story set",
		"satelle story set x --note \"a b\"",
		"satelle story set *",
		"satelle story set x # c",
		"",
	} {
		if Allowed(entries, c) {
			t.Errorf("%q allowed", c)
		}
	}
	if Allowed(nil, "satelle story set x") {
		t.Error("no entries allowed a command")
	}
	if Allowed([]string{"satelle *"}, "satelle story set x") {
		t.Error("an invalid entry allowed a command")
	}
}
