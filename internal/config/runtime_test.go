package config

import (
	"slices"
	"testing"
)

func TestSecretName(t *testing.T) {
	for _, n := range []string{"GH_TOKEN", "AWS_SECRET_ACCESS_KEY", "OTHER_PASSWORD", "STRIPE_API_KEY", "OPENAI_API_KEY", "AWS_ACCESS_KEY_ID", "GOOGLE_APPLICATION_CREDENTIALS", "MY_AUTH", "api_key_x"} {
		if !secretName(n) {
			t.Errorf("secretName(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"PATH", "HOME", "HTTPS_PROXY", "CLAUDE_CONFIG_DIR", "KEYBOARD"} {
		if secretName(n) {
			t.Errorf("secretName(%q) = true, want false", n)
		}
	}
}

func TestTargetAgentEnv(t *testing.T) {
	environ := []string{
		"PATH=/bin", "HOME=/home/x", "LC_ALL=C", "TERM=xterm", "GOCACHE=/tmp/gocache",
		"SATELLE_TOKEN=satelle-token-value", "SATELLE_NODE_TOKEN=node-token-value",
		"SATELLE_NODE_WITHHOLD=EXTRA_NAME,PATH_EXTRA", "EXTRA_NAME=extra-withheld-value",
		"GH_TOKEN=gh-token-value", "GITHUB_TOKEN=github-token-value",
		"AWS_ACCESS_KEY_ID=aws-access-key", "AWS_SECRET_ACCESS_KEY=aws-secret-value",
		"OTHER_PASSWORD=other-password-value", "STRIPE_API_KEY=stripe-key-value",
		"GIT_DIR=/ctl/.git", "GIT_CONFIG_PARAMETERS='core.pager=evil'",
		"ANTHROPIC_API_KEY=anthropic-key-value", "OPENAI_API_KEY=openai-key-value",
		"CLAUDE_CONFIG_DIR=/home/x/.claude", "PLAIN=plain", "ALL_PROXY=http://proxy:1",
		"NO_PROXY=localhost",
	}
	env, secrets := TargetAgentEnv(environ, []string{"ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"}, nil)
	want := []string{"PATH=/bin", "HOME=/home/x", "LC_ALL=C", "TERM=xterm",
		"ANTHROPIC_API_KEY=anthropic-key-value", "CLAUDE_CONFIG_DIR=/home/x/.claude", "NO_PROXY=localhost"}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	env, _ = TargetAgentEnv(environ, nil, []string{"GOCACHE", "PLAIN", "GH_TOKEN", "GIT_DIR", "SATELLE_TOKEN"})
	want = []string{"PATH=/bin", "HOME=/home/x", "LC_ALL=C", "TERM=xterm", "GOCACHE=/tmp/gocache", "PLAIN=plain", "NO_PROXY=localhost"}
	if !slices.Equal(env, want) {
		t.Errorf("env with configured names = %q, want %q", env, want)
	}
	for _, v := range []string{"anthropic-key-value", "/home/x/.claude", "satelle-token-value", "node-token-value",
		"extra-withheld-value", "gh-token-value", "github-token-value", "aws-access-key", "aws-secret-value",
		"other-password-value", "stripe-key-value", "openai-key-value"} {
		if !slices.Contains(secrets, v) {
			t.Errorf("secrets lack %q: %q", v, secrets)
		}
	}
	if slices.Contains(secrets, "plain") || slices.Contains(secrets, "/bin") {
		t.Errorf("secrets hold an ordinary value: %q", secrets)
	}
	if env, _ := TargetAgentEnv(nil, nil, nil); env == nil || len(env) != 0 {
		t.Errorf("TargetAgentEnv(nil) = %#v, want empty and non-nil", env)
	}
	env, _ = TargetAgentEnv([]string{"SATELLE_NODE_WITHHOLD=ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY=anthropic-key-value"}, []string{"ANTHROPIC_API_KEY"}, nil)
	if len(env) != 0 {
		t.Errorf("a withheld auth name is kept: %q", env)
	}
	keyring := []string{"PATH=/bin", "DISPLAY=:0", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"}
	if env, _ := TargetAgentEnv(keyring, nil, nil); !slices.Equal(env, []string{"PATH=/bin"}) {
		t.Errorf("the base allowlist keeps a keyring variable: %q", env)
	}
	if env, _ := TargetAgentEnv(keyring, []string{"DISPLAY", "DBUS_SESSION_BUS_ADDRESS"}, nil); !slices.Equal(env, keyring) {
		t.Errorf("keyring variables as auth names = %q, want %q", env, keyring)
	}
}

func TestTargetAgentEnvProxy(t *testing.T) {
	cases := []struct {
		value   string
		keep    bool
		secrets []string
	}{
		{"http://user:longpassword@host:3128", true, []string{"http://user:longpassword@host:3128", "longpassword"}},
		{"http://user:short@host:3128", false, nil},
		{"http://longusertoken@host:3128", true, []string{"http://longusertoken@host:3128", "longusertoken"}},
		{"http://user:long%40password@host:3128", true, []string{"http://user:long%40password@host:3128", "long@password", "long%40password"}},
		{"user:longpassword@host:3128", false, nil},
		{"http://host:3128", true, nil},
	}
	for _, c := range cases {
		env, secrets := TargetAgentEnv([]string{"HTTPS_PROXY=" + c.value}, nil, nil)
		if kept := len(env) == 1; kept != c.keep {
			t.Errorf("%s: kept = %v, want %v", c.value, kept, c.keep)
		}
		if !slices.Equal(secrets, c.secrets) {
			t.Errorf("%s: secrets = %q, want %q", c.value, secrets, c.secrets)
		}
	}
	env, secrets := TargetAgentEnv([]string{"no_proxy=localhost,127.0.0.1", "ALL_PROXY=http://user:longpassword@host:1", "http_proxy=http://p:3128"}, nil, nil)
	if !slices.Equal(env, []string{"no_proxy=localhost,127.0.0.1", "http_proxy=http://p:3128"}) {
		t.Errorf("env = %q", env)
	}
	if len(secrets) != 0 {
		t.Errorf("secrets = %q, want none", secrets)
	}
}

func TestGitLookupEnvKeepsConfigKeys(t *testing.T) {
	got := GitLookupEnv([]string{"GIT_CONFIG_KEY_0=core.x", "GIT_CONFIG_VALUE_0=y", "STRIPE_API_KEY=k"}, nil)
	if want := []string{"GIT_CONFIG_KEY_0=core.x", "GIT_CONFIG_VALUE_0=y"}; !slices.Equal(got, want) {
		t.Errorf("GitLookupEnv = %q, want %q", got, want)
	}
}

func TestTargetEnvProblem(t *testing.T) {
	for _, n := range []string{"GOPATH", "GOCACHE", "NODE_OPTIONS", "_X1"} {
		if err := TargetEnvProblem(n); err != nil {
			t.Errorf("TargetEnvProblem(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range []string{"GH_TOKEN", "STRIPE_API_KEY", "MY_AUTH", "GIT_DIR", "SATELLE_TOKEN", "A=B", "1X", "", "A B"} {
		if TargetEnvProblem(n) == nil {
			t.Errorf("TargetEnvProblem(%q) = nil, want a problem", n)
		}
	}
}
