package hosted

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func identityCred(url string) Credential {
	return Credential{
		ServerURL:    url,
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Scope:        "mcp",
		DisplayName:  "Ada",
		Email:        "ada@example.com",
		PrincipalID:  "usr_1",
		ExpiresAt:    "2026-10-06T01:00:00Z",
		CreatedAt:    "2026-10-06T00:00:00Z",
	}
}

func TestIdentityDiff(t *testing.T) {
	const keep = "https://keep.example.com"
	const other = "https://other.example.com"
	save := func(t *testing.T, s FileStore, cs ...Credential) {
		t.Helper()
		for _, c := range cs {
			if err := s.Save(c); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, s FileStore, path string)
		write func(t *testing.T, s FileStore, path string)
		want  string
	}{
		{"token refresh", func(t *testing.T, s FileStore, _ string) { save(t, s, identityCred(keep)) },
			func(t *testing.T, s FileStore, _ string) {
				c := identityCred(keep)
				c.AccessToken, c.RefreshToken = "access-2", "refresh-2"
				c.ExpiresAt, c.CreatedAt = "2026-10-06T02:00:00Z", "2026-10-06T01:00:00Z"
				save(t, s, c)
			}, ""},
		{"server added", func(t *testing.T, s FileStore, _ string) { save(t, s, identityCred(keep)) },
			func(t *testing.T, s FileStore, _ string) { save(t, s, identityCred(other)) }, "server added " + other},
		{"server removed", func(t *testing.T, s FileStore, _ string) { save(t, s, identityCred(keep), identityCred(other)) },
			func(t *testing.T, s FileStore, _ string) {
				if err := s.Delete(other); err != nil {
					t.Fatal(err)
				}
			}, "server removed " + other},
		{"email changed", func(t *testing.T, s FileStore, _ string) { save(t, s, identityCred(keep)) },
			func(t *testing.T, s FileStore, _ string) {
				c := identityCred(keep)
				c.Email = "grace@example.com"
				save(t, s, c)
			}, "identity changed for " + keep},
		{"principal changed", func(t *testing.T, s FileStore, _ string) { save(t, s, identityCred(keep)) },
			func(t *testing.T, s FileStore, _ string) {
				c := identityCred(keep)
				c.PrincipalID = "usr_9"
				save(t, s, c)
			}, "identity changed for " + keep},
		{"created from absent", func(*testing.T, FileStore, string) {},
			func(t *testing.T, s FileStore, _ string) { save(t, s, identityCred(keep)) }, "file appeared"},
		{"deleted", func(t *testing.T, s FileStore, _ string) { save(t, s, identityCred(keep)) },
			func(t *testing.T, _ FileStore, p string) {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			}, "file removed"},
		{"unparseable", func(t *testing.T, s FileStore, _ string) { save(t, s, identityCred(keep)) },
			func(t *testing.T, _ FileStore, p string) {
				if err := os.WriteFile(p, []byte("[[credential\nnot toml"), 0o600); err != nil {
					t.Fatal(err)
				}
			}, "unparseable"},
		{"absent stays absent", func(*testing.T, FileStore, string) {}, func(*testing.T, FileStore, string) {}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.toml")
			store := FileStore{Path: path}
			tc.setup(t, store, path)
			before := CaptureIdentity(path)
			tc.write(t, store, path)
			after := CaptureIdentity(path)
			reasons := DiffIdentity(before, after)
			if tc.want == "" {
				if len(reasons) != 0 {
					t.Fatalf("want no diff, got %v", reasons)
				}
				return
			}
			if len(reasons) != 1 || !strings.Contains(reasons[0], tc.want) {
				t.Fatalf("want one reason containing %q, got %v", tc.want, reasons)
			}
		})
	}
}

func TestIdentityTextRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	store := FileStore{Path: path}
	for _, u := range []string{"https://b.example.com/", "https://a.example.com"} {
		if err := store.Save(identityCred(u)); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]Identity{
		"ok":          CaptureIdentity(path),
		"absent":      CaptureIdentity(filepath.Join(t.TempDir(), "none.toml")),
		"unparseable": {State: IdentityUnparseable, Digest: "unreadable: a\tb\nc"},
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			text, err := id.MarshalText()
			if err != nil {
				t.Fatal(err)
			}
			var got Identity
			if err := got.UnmarshalText(text); err != nil {
				t.Fatal(err)
			}
			again, _ := got.MarshalText()
			if !reflect.DeepEqual(text, again) {
				t.Fatalf("re-encode differs:\n%q\n%q", text, again)
			}
			if name != "unparseable" && len(DiffIdentity(id, got)) != 0 {
				t.Fatalf("round trip changed identity: %v", DiffIdentity(id, got))
			}
		})
	}
	var bad Identity
	if err := bad.UnmarshalText([]byte("garbage\n")); err == nil {
		t.Fatal("malformed snapshot must not decode")
	}
}
