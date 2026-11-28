package shq

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestQuote(t *testing.T) {
	if got := Quote("a'b"); got != `'a'\''b'` {
		t.Fatalf("Quote(a'b) = %q", got)
	}
	if got := Quote(""); got != "''" {
		t.Fatalf("Quote(\"\") = %q", got)
	}
}

func TestQuoteRoundTripsThroughSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	in := "x $HOME `id` $(id) it's\nnext\x1b[0m \\"
	out, err := exec.Command(sh, "-c", "printf %s "+Quote(in)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("sh printed %q, want %q", out, in)
	}
}
