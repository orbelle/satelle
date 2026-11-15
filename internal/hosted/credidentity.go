package hosted

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	IdentityAbsent      = "absent"
	IdentityOK          = "ok"
	IdentityUnparseable = "unparseable"
)

type Identity struct {
	State   string
	Digest  string
	Servers map[string]string
}

func CaptureIdentity(path string) Identity {
	if path == "" {
		return Identity{State: IdentityAbsent}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Identity{State: IdentityAbsent}
		}
		return Identity{State: IdentityUnparseable, Digest: "unreadable:" + err.Error()}
	}
	var cf struct {
		Credential []Credential `toml:"credential"`
	}
	if err := toml.Unmarshal(b, &cf); err != nil {
		sum := sha256.Sum256(b)
		return Identity{State: IdentityUnparseable, Digest: hex.EncodeToString(sum[:])}
	}
	servers := map[string]string{}
	for _, c := range cf.Credential {
		url := strings.TrimRight(strings.TrimSpace(c.ServerURL), "/")
		sum := sha256.Sum256([]byte(c.DisplayName + "\x00" + c.Email + "\x00" + c.PrincipalID))
		servers[url] += hex.EncodeToString(sum[:])
	}
	return Identity{State: IdentityOK, Servers: servers}
}

func DiffIdentity(before, after Identity) []string {
	if before.State != after.State {
		switch {
		case before.State == IdentityAbsent:
			return []string{"file appeared"}
		case after.State == IdentityAbsent:
			return []string{"file removed"}
		case after.State == IdentityUnparseable:
			return []string{"unparseable"}
		default:
			return []string{"was unparseable, now parses"}
		}
	}
	switch before.State {
	case IdentityUnparseable:
		if before.Digest != after.Digest {
			return []string{"unparseable content changed"}
		}
	case IdentityOK:
		urls := map[string]struct{}{}
		for u := range before.Servers {
			urls[u] = struct{}{}
		}
		for u := range after.Servers {
			urls[u] = struct{}{}
		}
		sorted := make([]string, 0, len(urls))
		for u := range urls {
			sorted = append(sorted, u)
		}
		sort.Strings(sorted)
		var reasons []string
		for _, u := range sorted {
			b, bok := before.Servers[u]
			a, aok := after.Servers[u]
			switch {
			case !bok:
				reasons = append(reasons, "server added "+u)
			case !aok:
				reasons = append(reasons, "server removed "+u)
			case a != b:
				reasons = append(reasons, "identity changed for "+u)
			}
		}
		return reasons
	}
	return nil
}

func (id Identity) MarshalText() ([]byte, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "state\t%s\n", id.State)
	if id.State == IdentityUnparseable {
		fmt.Fprintf(&sb, "digest\t%s\n", strings.NewReplacer("\t", " ", "\n", " ").Replace(id.Digest))
	}
	urls := make([]string, 0, len(id.Servers))
	for u := range id.Servers {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	for _, u := range urls {
		fmt.Fprintf(&sb, "server\t%s\t%s\n", id.Servers[u], strings.NewReplacer("\n", " ").Replace(u))
	}
	return []byte(sb.String()), nil
}

func (id *Identity) UnmarshalText(text []byte) error {
	out := Identity{}
	for _, line := range strings.Split(strings.TrimRight(string(text), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		switch {
		case parts[0] == "state" && len(parts) == 2:
			out.State = parts[1]
		case parts[0] == "digest" && len(parts) == 2:
			out.Digest = parts[1]
		case parts[0] == "server" && len(parts) == 3:
			if out.Servers == nil {
				out.Servers = map[string]string{}
			}
			out.Servers[parts[2]] = parts[1]
		default:
			return fmt.Errorf("hosted: malformed identity line %q", line)
		}
	}
	switch out.State {
	case IdentityAbsent, IdentityOK, IdentityUnparseable:
	default:
		return fmt.Errorf("hosted: identity has no valid state %q", out.State)
	}
	*id = out
	return nil
}
