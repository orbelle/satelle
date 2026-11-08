package ledgerstore

import (
	"strings"
	"testing"
)

func TestKeyLengthBound(t *testing.T) {
	if err := CheckKey(strings.Repeat("a", MaxKeyBytes)); err != nil {
		t.Errorf("a 512-byte key: %v", err)
	}
	err := CheckKey(strings.Repeat("a", MaxKeyBytes+1))
	if err == nil || !strings.Contains(err.Error(), "the limit is 512 bytes") || !strings.Contains(err.Error(), "513 bytes") {
		t.Errorf("a 513-byte key: %v", err)
	}
	if err := CheckKey(""); err == nil {
		t.Error("the empty key was accepted")
	}
}

func TestKeyCharset(t *testing.T) {
	for _, k := range []string{"a b", "a%b", "a:b", "café", "a\x00b", "a\\b", "a\nb"} {
		err := CheckKey(k)
		if err == nil || !strings.Contains(err.Error(), "[A-Za-z0-9._/-]") {
			t.Errorf("key %q: %v", k, err)
		}
	}
	for _, k := range []string{"aZ09._/-", "chains/sto_x/000000000001.json", "blobs/" + strings.Repeat("f", 64), ".", "..", "-"} {
		if err := CheckKey(k); err != nil {
			t.Errorf("key %q: %v", k, err)
		}
	}
}

func TestBodyBound(t *testing.T) {
	if err := CheckBody(64 << 20); err != nil {
		t.Errorf("a 64 MiB body: %v", err)
	}
	err := CheckBody(64<<20 + 1)
	if err == nil || !strings.Contains(err.Error(), "the limit is 64 MiB") {
		t.Errorf("a 64 MiB + 1 body: %v", err)
	}
}
