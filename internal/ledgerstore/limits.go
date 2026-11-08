package ledgerstore

import "fmt"

// The contract's limits: keys are ASCII [A-Za-z0-9._/-] of at most
// MaxKeyBytes bytes, and bodies are at most MaxBodyBytes bytes.
const (
	MaxKeyBytes  = 512
	MaxBodyBytes = 64 << 20
)

// CheckKey refuses a key the contract does not allow: empty, longer than
// MaxKeyBytes, or holding a byte outside ASCII [A-Za-z0-9._/-]. A list prefix
// and an after-key are checked by the same rule.
func CheckKey(k string) error {
	if k == "" {
		return fmt.Errorf("key is empty; keys are ASCII [A-Za-z0-9._/-], 1 to %d bytes", MaxKeyBytes)
	}
	if len(k) > MaxKeyBytes {
		return fmt.Errorf("key is %d bytes; the limit is %d bytes", len(k), MaxKeyBytes)
	}
	for i := 0; i < len(k); i++ {
		if !keyByte(k[i]) {
			return fmt.Errorf("key %q has byte 0x%02x at offset %d; keys are ASCII [A-Za-z0-9._/-]", k, k[i], i)
		}
	}
	return nil
}

func keyByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return c == '.' || c == '_' || c == '/' || c == '-'
}

// CheckBody refuses a body of n bytes when n is over MaxBodyBytes.
func CheckBody(n int) error {
	if n > MaxBodyBytes {
		return fmt.Errorf("body is %d bytes; the limit is 64 MiB (%d bytes)", n, MaxBodyBytes)
	}
	return nil
}
