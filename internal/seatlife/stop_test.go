package seatlife

import (
	"os"
	"path/filepath"
	"testing"
)

func marked(home, session string) bool {
	_, err := os.Stat(filepath.Join(home, Dir, session+stopSuffix))
	return err == nil
}

// The stop marker needs no session record: a stop that owes a move blocks
// once, the stop straight after passes, and a third blocks again.
func TestStopBlocksOnceWithoutARecord(t *testing.T) {
	home := t.TempDir()
	const s = "sess-1"
	if !Stop(home, s, true) {
		t.Fatal("the first owed stop was let through")
	}
	if !marked(home, s) {
		t.Fatal("a blocked stop left no marker")
	}
	if Stop(home, s, true) {
		t.Fatal("the second owed stop in a row was blocked")
	}
	if marked(home, s) {
		t.Fatal("the second stop left the marker")
	}
	if !Stop(home, s, true) {
		t.Fatal("the third owed stop was not blocked again")
	}
	if _, ok := Load(home, s); ok {
		t.Fatal("Stop wrote a session record")
	}
}

func TestStopOwedThenNotOwedClearsTheMarker(t *testing.T) {
	home := t.TempDir()
	const s = "sess-2"
	if !Stop(home, s, true) {
		t.Fatal("the owed stop was let through")
	}
	if Stop(home, s, false) {
		t.Fatal("a stop that owes nothing was blocked")
	}
	if marked(home, s) {
		t.Fatal("a stop that owes nothing left the marker")
	}
	if !Stop(home, s, true) {
		t.Fatal("an owed stop after a clear pass was not blocked")
	}
	if Stop(t.TempDir(), s, false) {
		t.Fatal("a stop that owes nothing, with no marker, was blocked")
	}
}

func TestStopLetsThroughWhatItCannotMark(t *testing.T) {
	file := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Stop(file, "sess-3", true) {
		t.Fatal("a stop whose marker cannot be written was blocked")
	}
	if Stop(t.TempDir(), "../x", true) {
		t.Fatal("a session naming a path was blocked")
	}
	if Stop("", "sess-3", true) {
		t.Fatal("a stop with no satelle home was blocked")
	}
}

func TestForgetAndPruneRemoveTheMarker(t *testing.T) {
	home := t.TempDir()
	for _, s := range []string{"forgot", "kept", "dropped"} {
		if !Stop(home, s, true) {
			t.Fatalf("%s: the owed stop was let through", s)
		}
	}
	if err := Forget(home, "forgot"); err != nil {
		t.Fatal(err)
	}
	if marked(home, "forgot") {
		t.Fatal("Forget left the marker")
	}
	if err := Prune(home, func(s string) bool { return s == "kept" }); err != nil {
		t.Fatal(err)
	}
	if !marked(home, "kept") {
		t.Fatal("Prune removed a marker keep holds")
	}
	if marked(home, "dropped") {
		t.Fatal("Prune kept a marker with no record that keep rejects")
	}
}
