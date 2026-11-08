package config

import (
	"os"
	"strings"
)

// OSUser is the operating-system user running this process: USER, else
// USERNAME, else empty. It is the one reader of those variables.
func OSUser() string {
	for _, k := range []string{"USER", "USERNAME"} {
		if u := strings.TrimSpace(os.Getenv(k)); u != "" {
			return u
		}
	}
	return ""
}
