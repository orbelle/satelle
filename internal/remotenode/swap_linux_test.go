package remotenode

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// recordSwap replaces SwapRunner with a recorder that fails the argv whose
// command is failOn ("" fails none), and returns what it recorded.
func recordSwap(t *testing.T, failOn string) *[][]string {
	t.Helper()
	var mu sync.Mutex
	got := &[][]string{}
	prev := SwapRunner
	SwapRunner = func(_ context.Context, argv []string) error {
		mu.Lock()
		defer mu.Unlock()
		*got = append(*got, argv)
		if argv[0] == failOn {
			return errors.New(argv[0] + ": exit status 1")
		}
		return nil
	}
	t.Cleanup(func() { SwapRunner = prev })
	return got
}

// AC5: a swap size makes a fresh swapfile of exactly that size and turns it on;
// disable turns it off and removes it.
func TestEnableSwapMakesAFreshSwapfileAndRemovesIt(t *testing.T) {
	got := recordSwap(t, "")
	disable, err := EnableSwap(context.Background(), "2G")
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 5 {
		t.Fatalf("argv = %v, want mkdir, fallocate, chmod, mkswap, swapon", *got)
	}
	file := (*got)[1][3]
	if !strings.HasPrefix(file, SwapDir+"/satelle-") {
		t.Errorf("swapfile = %q, want a fresh file under %s", file, SwapDir)
	}
	want := [][]string{
		{"mkdir", "-p", SwapDir},
		{"fallocate", "-l", "2G", file},
		{"chmod", "600", file},
		{"mkswap", file},
		{"swapon", file},
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("argv = %v, want %v", *got, want)
	}
	if err := disable(); err != nil {
		t.Fatal(err)
	}
	want = append(want, []string{"swapoff", file}, []string{"rm", "-f", file})
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("argv after disable = %v, want %v", *got, want)
	}

	// Each job gets its own file.
	again, err := EnableSwap(context.Background(), "2G")
	if err != nil {
		t.Fatal(err)
	}
	_ = again()
	if second := (*got)[8][3]; second == file {
		t.Errorf("a second job reused swapfile %q", file)
	}
}

func TestEnableSwapWithNoSizeIsANoOp(t *testing.T) {
	got := recordSwap(t, "")
	disable, err := EnableSwap(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := disable(); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("argv = %v, want none", *got)
	}
}

// A failing step is returned naming it, and the file made so far is removed.
func TestEnableSwapFailureRemovesTheFile(t *testing.T) {
	got := recordSwap(t, "swapon")
	disable, err := EnableSwap(context.Background(), "8G")
	if err == nil || disable != nil || !strings.Contains(err.Error(), "swap 8G: swapon") {
		t.Fatalf("EnableSwap = %v, %v; want the swapon failure", disable != nil, err)
	}
	last := (*got)[len(*got)-1]
	if last[0] != "rm" || last[2] != (*got)[1][3] {
		t.Errorf("last argv = %v, want the swapfile removed", last)
	}
}

// disable removes the file even when swapoff fails, and reports the failure.
func TestDisableSwapRemovesTheFileWhenSwapoffFails(t *testing.T) {
	got := recordSwap(t, "swapoff")
	disable, err := EnableSwap(context.Background(), "1M")
	if err != nil {
		t.Fatal(err)
	}
	if err := disable(); err == nil {
		t.Error("disable hid the swapoff failure")
	}
	if last := (*got)[len(*got)-1]; last[0] != "rm" {
		t.Errorf("last argv = %v, want the swapfile removed", last)
	}
}
