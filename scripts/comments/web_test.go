package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJSCommentsKeepStringsRegexAndTemplates(t *testing.T) {
	src := "// head\n(function () {\n  var u = \"http://x\"; // trailing\n  var r = s.split(/\\s+\\/\\//);\n  var q = '/* not */';\n  var t = `a // ${b /* in */ + \"}\"} // c`;\n  /* block\n     two */\n  return x / 2 / y;\n})();\n"
	want := "(function () {\n  var u = \"http://x\";\n  var r = s.split(/\\s+\\/\\//);\n  var q = '/* not */';\n  var t = `a // ${b   + \"}\"} // c`;\n  return x / 2 / y;\n})();\n"
	out, found, err := stripWeb([]byte(src), jsComments)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 4 {
		t.Fatalf("found %d comments, want 4: %v", len(found), found)
	}
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	again, found, err := stripWeb(out, jsComments)
	if err != nil || len(found) != 0 || string(again) != want {
		t.Fatalf("second strip changed the file: %v %v", found, err)
	}
}

func TestCSSComments(t *testing.T) {
	src := "/* head\n   two */\na { content: \"/* not */\"; } /* trailing */\nb { src: url(\"x.woff2\") /* mid */ format(\"woff2\"); }\n"
	want := "a { content: \"/* not */\"; }\nb { src: url(\"x.woff2\")   format(\"woff2\"); }\n"
	out, found, err := stripWeb([]byte(src), cssComments)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 3 || string(out) != want {
		t.Fatalf("found %d, got:\n%s\nwant:\n%s", len(found), out, want)
	}
}

func TestCheckNamesAJSAndACSSComment(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"a.js": "var x = 1; // added\n", "a.css": "a {}\n/* added */\n"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if code := run([]string{"check", p}); code != 1 {
			t.Fatalf("check on %s exited %d, want 1", name, code)
		}
	}
}
