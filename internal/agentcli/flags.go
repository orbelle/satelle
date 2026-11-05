package agentcli

import (
	"os"
	"path/filepath"
	"strings"
)

// flagValues returns the value of every occurrence of --name in args, in both the
// "--name v" and "--name=v" forms; a flag with no value contributes "".
func flagValues(args []string, name string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == name:
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				out = append(out, args[i+1])
				i++
			} else {
				out = append(out, "")
			}
		case strings.HasPrefix(a, name+"="):
			out = append(out, strings.TrimPrefix(a, name+"="))
		}
	}
	return out
}

// acpUnavailableUsage is the usage an ACP prompt result records when the
// session/prompt response carries none.
func acpUnavailableUsage(adapter string) UsageResult {
	return unavailableUsage("acp", "session/prompt response carried no usage token fields")
}

// sessionEnvKeys are the environment variables an adapter publishes its session
// id in, in the order they are asked.
var sessionEnvKeys = []string{claudeCodeSessionIDEnv}

// claudeCodeSessionIDEnv is where Claude Code publishes its session id.
const claudeCodeSessionIDEnv = "CLAUDE_CODE_SESSION_ID"

// claudeConfigDir/grokHomeDir resolve each harness's home
// directory, honouring an env override so tests (and an unusual install) can
// point at testdata instead of the real user home.
func claudeConfigDir() string {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); v != "" {
		return v
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".claude")
	}
	return ".claude"
}

func piHomeDir() string {
	if v := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); v != "" {
		return v
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".pi", "agent")
	}
	return filepath.Join(".pi", "agent")
}

// claudeProjectSlug mirrors Claude Code's own project directory naming: the
// absolute repo path with every separator turned into a dash (observed
// directly off this session's own memory path,
// ~/.claude/projects/-home-*-Development-satelle/ — sty_81caa41b).
func claudeProjectSlug(repoRoot string) string {
	return strings.ReplaceAll(filepath.Clean(repoRoot), string(filepath.Separator), "-")
}

// SessionIDFromEnv returns the session id an adapter publishes in environ
// (KEY=VALUE entries), "" when none does. The claude adapter answers.
func SessionIDFromEnv(environ []string) string {
	for _, want := range sessionEnvKeys {
		for _, e := range environ {
			key, val, _ := strings.Cut(e, "=")
			if key != want {
				continue
			}
			if id := strings.TrimSpace(val); id != "" {
				return id
			}
		}
	}
	return ""
}
