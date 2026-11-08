package hosted

import (
	"path/filepath"
	"testing"
)

func TestSignedIn(t *testing.T) {
	const server = "https://satelle.example"
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv(SessionTokenEnv, "")
	if SignedIn(server) {
		t.Fatal("signed in with no token and no stored login")
	}
	t.Setenv(SessionTokenEnv, "tok")
	if !SignedIn(server) {
		t.Fatal("not signed in with SATELLE_TOKEN set")
	}
	t.Setenv(SessionTokenEnv, "")
	if err := (FileStore{}).Save(Credential{ServerURL: server, AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	if !SignedIn(server) {
		t.Fatal("not signed in with a stored login for the server")
	}
	if SignedIn("https://other.example") {
		t.Fatal("signed in to a server with no stored login")
	}
}
