//go:build integration && unix

package tests

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const fakeSprite = `#!/bin/sh
ROOT='@ROOT@'
echo "$*" >> "$ROOT/log"
{ echo "== $*"; env; } >> "$ROOT/env.log"
last() { for a in "$@"; do l=$a; done; echo "$l"; }
case "$1" in
  org) echo "Currently selected org: test" ;;
  create) n=$(last "$@"); mkdir -p "$ROOT/$n/node" "$ROOT/$n/home" "$ROOT/$n/usr/local/bin" ;;
  api)
    n=$(echo "$2" | sed -n 's#^/v1/sprites/\([^/]*\)/policy/network$#\1#p')
    d=; prev=
    for a in "$@"; do [ "$prev" = -d ] && d=$a; prev=$a; done
    if [ -n "$d" ]; then printf '%s\n' "$d" >"$ROOT/policy.$n"; else cat "$ROOT/policy.$n"; fi ;;
  exec)
    shift; [ "$1" = -s ] || exit 2
    n=$2; shift 2
    [ "$1" = -- ] && shift
    [ "$1" = sudo ] && shift
    [ -d "$ROOT/$n" ] || { echo "fake sprite: no sprite $n" >&2; exit 1; }
    if [ "$*" = "tar -C /release -cf - back" ]; then
      [ -f "$ROOT/fail-back" ] && exit 1
      [ -f "$ROOT/back.tar" ] && exec cat "$ROOT/back.tar"
    fi
    m=$#
    for a in "$@"; do
      case "$a" in /node*|/release*|/usr/local/bin*) a="$ROOT/$n$a" ;; esac
      set -- "$@" "$a"
    done
    shift $m
    PATH="$ROOT/$n/usr/local/bin:$ROOT/nodebin:$PATH" HOME="$ROOT/$n/home" SATELLE_RELEASE_ROOT="$ROOT/$n/release" exec "$@" ;;
  destroy) n=$(last "$@"); echo "destroyed $n" >>"$ROOT/log"; rm -rf "$ROOT/$n" ;;
  list) for d in "$ROOT"/satelle-*; do [ -d "$d" ] && basename "$d"; done; exit 0 ;;
  *) echo "fake sprite: $*" >&2; exit 2 ;;
esac
`

const spriteToken = "ghp_SpriteDummy0123456789abcdefghijKLMNOP"

type spriteBox struct {
	*binarySandbox
	root  string
	ntask string
}

func installFakeSprite(t *testing.T, s *sandbox) string {
	t.Helper()
	root, bin := realDir(t, t.TempDir()), realDir(t, t.TempDir())
	writeFile(t, filepath.Join(bin, "sprite"), strings.ReplaceAll(fakeSprite, "@ROOT@", root), 0o755)
	nodebin := filepath.Join(root, "nodebin")
	realGrep, _ := exec.LookPath("grep")
	writeFile(t, filepath.Join(nodebin, "id"), "#!/bin/sh\n[ \"$1\" = -u ] && { echo 0; exit 0; }\nexec /usr/bin/id \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(nodebin, "chown"), "#!/bin/sh\necho \"chown $*\" >>'"+root+"/chown.log'\n", 0o755)
	writeFile(t, filepath.Join(nodebin, "runuser"), "#!/bin/sh\n{ echo \"== runuser $*\"; env; } >>'"+root+"/runuser.log'\nwhile [ $# -gt 0 ] && [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(nodebin, "grep"), "#!/bin/sh\necho \"grep $*\" >>'"+root+"/grep.log'\nexec '"+realGrep+"' \"$@\"\n", 0o755)
	s.path = append([]string{bin}, s.path...)
	return root
}

func copyRepoFile(t *testing.T, s *sandbox, rel string) {
	t.Helper()
	s.write(rel, repoText(t, rel))
}

func newSpriteBox(t *testing.T) *spriteBox {
	t.Helper()
	b := newBinarySandbox(t)
	ntask := b.create("task", "--title", "Release node part", "--input-kind", "story", "--input-tag", "release-node").ID
	for _, rel := range []string{
		".satelle/skills/satelle-node-sprite/SKILL.md", ".satelle/skills/satelle-node-sprite/pack.toml",
		".satelle/skills/satelle-release-node/SKILL.md", ".satelle/skills/satelle-release-node/pack.toml",
		".satelle/skills/satelle-change-judged-check.md", "scripts/release-node-verify.sh",
		".satelle/workflows/release-node.toml", ".satelle/remotes/release-sprite/satelle.toml",
	} {
		copyRepoFile(t, b.sandbox, rel)
	}
	for _, dir := range []string{".satelle/skills/satelle-node-sprite/scripts", ".satelle/skills/satelle-release-node/scripts"} {
		ents, err := os.ReadDir(filepath.Join("..", dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			copyRepoFile(t, b.sandbox, dir+"/"+e.Name())
		}
	}
	def := repoText(t, ".satelle/remotes/release-sprite/remote.toml")
	def = regexp.MustCompile(`(?m)^task = ".*"$`).ReplaceAllString(def, `task = "`+ntask+`"`)
	def = regexp.MustCompile(`(?m)^on_create = .*$`).ReplaceAllString(def, `on_create = ["satelle version"]`)
	b.write(".satelle/remotes/release-sprite/remote.toml", def)
	b.must("validate")
	b.write(".gitignore", ".satelle/.env\n")
	b.git("add", ".satelle", ".gitignore")
	b.git("commit", "-q", "-m", "the release's node part")
	b.git("push", "-q", "origin", "HEAD:refs/heads/main")
	b.git("fetch", "-q", "origin")
	b.base = b.rev("HEAD")
	b.write(".satelle/.env", "GH_TOKEN="+spriteToken+"\n")
	root := installFakeSprite(t, b.sandbox)
	path := ""
	for _, kv := range b.environ() {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	b.env["GH_TOKEN"] = ""
	b.env["SATELLE_RELEASE_SUITE_PATH"] = path
	b.env["SATELLE_RELEASE_SUITE_HOME"] = b.suiteHome
	b.env["SATELLE_RELEASE_POLL"] = "0"
	return &spriteBox{binarySandbox: b, root: root, ntask: ntask}
}

func (x *spriteBox) file(name string) string {
	x.t.Helper()
	b, err := os.ReadFile(filepath.Join(x.root, name))
	if err != nil && !os.IsNotExist(err) {
		x.t.Fatal(err)
	}
	return string(b)
}

func (x *spriteBox) release() (run, cand string, fire, read result) {
	x.t.Helper()
	run, cand, fire = x.fireNode()
	read = x.block("\n### Read the node's result\n")
	return run, cand, fire, read
}

func (x *spriteBox) fireNode() (run, cand string, fire result) {
	x.t.Helper()
	run, _ = x.fire()
	x.mustBlock("### Build the candidate")
	x.mustBlock("### Send back")
	x.driftGate()
	x.mustBlock(nodeInputHeading)
	cand = strings.TrimSpace(x.exec("cat", "", filepath.Join(x.rd, "drift-checked")).out)
	x.git("checkout", "-q", "--detach", cand)
	fire = x.block("\n### Fire the node's part\n")
	return run, cand, fire
}

func (x *spriteBox) status() map[string]any {
	x.t.Helper()
	var st map[string]any
	if err := json.Unmarshal([]byte(x.must("remote", "status", "release-sprite", x.ntask, "--json")), &st); err != nil {
		x.t.Fatal(err)
	}
	return st
}

func (x *spriteBox) noToken(where string, texts ...string) {
	x.t.Helper()
	for _, s := range texts {
		if strings.Contains(s, spriteToken) {
			x.t.Fatalf("the token is in %s", where)
		}
	}
}

func (x *spriteBox) noTokenUnder(dir string) {
	x.t.Helper()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(spriteToken)) {
			x.t.Fatalf("the token is in %s", p)
		}
		return nil
	})
}

func TestReleaseRunsItsNodePartOnASprite(t *testing.T) {
	x := newSpriteBox(t)
	id, _ := x.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 4 }\n"})
	oldMain := x.rev("origin/main")
	run, cand, fire, read := x.release()
	if fire.code != 0 || read.code != 0 {
		t.Fatalf("fire: exit %d\n%s%s\nread: exit %d\n%s%s\nlog:\n%s", fire.code, fire.out, fire.err, read.code, read.out, read.err, x.file("log"))
	}
	node := nodeOf(t, fire.out)
	sn := "satelle-" + strings.ReplaceAll(node, "_", "-")
	assertInOrder(t, x.file("log"),
		"org list\n",
		"create --skip-console "+sn+"\n",
		"exec -s "+sn+" -- sudo sh -c ", " /release/in node-in\n",
		"api /v1/sprites/"+sn+"/policy/network -- -s -f -X POST",
		"exec -s "+sn+" -- sudo satelle node run-local --dir /node\n",
		"exec -s "+sn+" -- sudo tar -C /release -cf - back\n",
		"destroy --force "+sn+"\n",
	)
	if st := x.status(); st["verdict"] != "accept" || !strings.Contains(st["notes"].(string), "secret-scan clean") {
		t.Fatalf("remote status: %v", st)
	}
	execs := executionsOf(x.sandbox, x.ntask)
	if len(execs) != 1 || execs[0].Status != "done" {
		t.Fatalf("node executions %+v", execs)
	}
	byNode := false
	for _, e := range chainOf(x.sandbox, execs[0].ID) {
		byNode = byNode || e.Actor == "node:"+node
	}
	if !byNode {
		t.Fatalf("the node execution %s carries no event by node:%s", execs[0].ID, node)
	}
	rn := x.must("execution", "attachment", run, "release-node")
	for _, want := range []string{"node-execution " + execs[0].ID + "\n", "privilege release\n", "secret-scan clean\n", "verdict accept", "sprite " + sn + " is not listed", "verify: ok", "tested " + cand + "\n"} {
		if !strings.Contains(rn, want) {
			t.Fatalf("release-node lacks %q:\n%s", want, rn)
		}
	}
	x.git("fetch", "-q", "origin", "--tags")
	main := x.rev("origin/main")
	if x.rev(main+"^") != cand || x.rev("v1.0.1^{commit}") != main {
		t.Fatalf("origin/main %s is not one bump on the tested candidate %s with v1.0.1 at it", main, cand)
	}
	if want := "the node's part landed: main is " + main + "\n"; !strings.HasSuffix(read.out, want) {
		t.Fatalf("the read block's output does not end %q:\n%s", want, read.out)
	}
	if !strings.Contains(x.fakeLog("gh.log"), "main at dispatch "+oldMain+"\n") {
		t.Fatalf("release.yml was not dispatched before main moved:\n%s", x.fakeLog("gh.log"))
	}

	sum := sha256.Sum256([]byte(spriteToken))
	if !strings.Contains(x.fakeLog("gh.log"), "token sha256 "+hex.EncodeToString(sum[:])+"\n") {
		t.Fatalf("the node's gh was not given the token from .satelle/.env:\n%s", x.fakeLog("gh.log"))
	}
	x.noToken("the sprite's argv or [connect] environment", x.file("log"), x.file("env.log"))
	x.noToken("the node's grep argv", x.file("grep.log"))
	if !strings.Contains(x.file("grep.log"), "-f /dev/fd/3") {
		t.Fatalf("the node's secret scan did not read the token on fd 3:\n%s", x.file("grep.log"))
	}
	if ru := x.file("runuser.log"); !strings.Contains(ru, "== runuser -u release -- make -C ") || strings.Contains(ru, "GH_TOKEN") {
		t.Fatalf("the suite's runuser environment:\n%s", ru)
	}
	x.noTokenUnder(x.env["SATELLE_HOME"])
	for _, name := range []string{"release-node", "release-binary", "release-kept", "release-drift"} {
		x.noToken("the run's "+name, x.must("execution", "attachment", run, name))
	}
	if strings.Contains(x.file("chown.log"), "/release/work") || strings.Contains(x.file("chown.log"), "/release/out") {
		t.Fatalf("the node gave root's trees away:\n%s", x.file("chown.log"))
	}

	block := bindSession(fencedAfter(t, "## 7."))
	for k, v := range map[string]string{"<ver>": "1.0.1", `<the path in the [...] of the "installed" line of satelle service status>`: filepath.Join(x.fake, "satelled")} {
		block = strings.ReplaceAll(block, k, v)
	}
	if r := x.exec("bash", "", "-c", block); r.code != 0 {
		t.Fatalf("## 7.: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := x.done(id); r.code != 0 {
		t.Fatalf("done %s: exit %d\n%s%s", id, r.code, r.out, r.err)
	}
	for _, gate := range append([]string{"release-run"}, releaseGates...) {
		if g := latestRun(x.sandbox, id, gate); g.Result != "accept" {
			t.Fatalf("%s on %s: %+v", gate, id, g)
		}
	}
}

func TestReleaseSpriteRejectsAFailedNodePart(t *testing.T) {
	x := newSpriteBox(t)
	x.story("r", "### Fixed\n- R breaks the suite\n", [2]string{"Makefile", "test:\n\t@echo '--- FAIL: TestRed (0.00s)'\n\texit 1\n"})
	oldMain := x.rev("origin/main")
	run, _, fire, read := x.release()
	if fire.code == 0 || !strings.Contains(fire.out, "verdict: reject") {
		t.Fatalf("fire: exit %d, want the reject's non-zero exit\n%s%s", fire.code, fire.out, fire.err)
	}
	if read.code == 0 || !strings.Contains(read.err, "is cancelled and its items stay approved") {
		t.Fatalf("read: exit %d\n%s%s", read.code, read.out, read.err)
	}
	if st := x.status(); st["verdict"] != "reject" || !strings.Contains(st["notes"].(string), "failed step test: suite-exit") {
		t.Fatalf("remote status: %v", st)
	}
	node := nodeOf(t, fire.out)
	if !strings.Contains(x.file("log"), "destroyed satelle-"+strings.ReplaceAll(node, "_", "-")+"\n") {
		t.Fatalf("the sprite was not destroyed:\n%s", x.file("log"))
	}
	var runs []execRow
	x.mustJSON(&runs, "execution", "list", "--parent", x.task.ID)
	for _, r := range runs {
		if r.ID == run && r.Status != "cancelled" {
			t.Fatalf("the run %s is %s, want cancelled", run, r.Status)
		}
	}
	ev := x.must("execution", "attachment", run, "release-evidence")
	if !strings.Contains(ev, "failed step test: suite-exit") || !strings.Contains(ev, "--- FAIL: TestRed") {
		t.Fatalf("release-evidence:\n%s", ev)
	}
	x.noToken("release-evidence", ev)
	x.mainStays(t, oldMain)
	if r := x.exec("git", "", "ls-remote", "origin", "refs/heads/release/"+run); strings.TrimSpace(r.out) != "" {
		t.Fatalf("release/%s survives a rejected run", run)
	}
}

func TestReleaseSpriteDefinition(t *testing.T) {
	var def struct {
		Provider string `toml:"provider"`
		Connect  struct {
			Start, Stop []string
		} `toml:"connect"`
		Secrets struct {
			Names    []string       `toml:"names"`
			Override map[string]any `toml:"override"`
		} `toml:"secrets"`
		Setup struct {
			OnCreate []string `toml:"on_create"`
		} `toml:"setup"`
		Network struct {
			Allow []string `toml:"allow"`
		} `toml:"network"`
		Lifecycle struct {
			Scope string `toml:"scope"`
		} `toml:"lifecycle"`
	}
	text := repoText(t, ".satelle/remotes/release-sprite/remote.toml")
	if _, err := toml.Decode(text, &def); err != nil {
		t.Fatal(err)
	}
	if def.Provider != "command" || !slices.Equal(def.Secrets.Names, []string{"GH_TOKEN"}) || def.Secrets.Override != nil || def.Lifecycle.Scope != "run" {
		t.Fatalf("release-sprite: %+v", def)
	}
	for _, h := range []string{"github.com", "api.github.com", "proxy.golang.org", "sum.golang.org", "dl.google.com", "archive.ubuntu.com"} {
		if !slices.Contains(def.Network.Allow, h) {
			t.Errorf("[network] allow lacks %s", h)
		}
	}
	for _, c := range def.Setup.OnCreate {
		if strings.Contains(c, ".sh") && !strings.HasPrefix(c, "env -u GH_TOKEN ") {
			t.Errorf("[setup] %q runs a script with the token in its environment", c)
		}
	}
	if !strings.Contains(strings.Join(def.Connect.Start, " "), "satelle-release-node/scripts/start.sh") || !strings.Contains(strings.Join(def.Connect.Stop, " "), "satelle-release-node/scripts/stop.sh") {
		t.Errorf("release-sprite's start and stop are not the release wrappers: %v %v", def.Connect.Start, def.Connect.Stop)
	}
	shaped := regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`)
	for _, dir := range []string{"../.satelle/remotes/release-sprite", "../.satelle/skills/satelle-release-node"} {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if b, _ := os.ReadFile(p); shaped.Match(b) {
					t.Errorf("%s holds a token-shaped string", p)
				}
			}
			return nil
		})
	}
	for _, rel := range []string{"start.sh", "stop.sh"} {
		if strings.Contains(repoText(t, ".satelle/skills/satelle-release-node/scripts/"+rel), "GH_TOKEN") {
			t.Errorf("%s names GH_TOKEN", rel)
		}
	}
	node := repoText(t, releaseNodeScript)
	if !strings.Contains(node, `CRED=(-c credential.helper= -c 'credential.https://github.com.helper=`) {
		t.Error("release-node.sh does not clear credential.helper before its github.com helper")
	}
	if !strings.Contains(node, `GITC=(-c core.hooksPath=/dev/null -c core.fsmonitor=)`) {
		t.Error("release-node.sh does not fix core.hooksPath and core.fsmonitor")
	}
	for _, l := range strings.Split(node, "\n") {
		if strings.Contains(l, "git ") && (strings.Contains(l, `"$WORK"`) || strings.Contains(l, `"$SUITE"`)) && !strings.Contains(l, `"${GITC[@]}"`) {
			t.Errorf("a git call in the node's trees without the fixed options: %s", strings.TrimSpace(l))
		}
	}
}

func spriteArchive(t *testing.T, hdrs ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hdrs {
		h := h
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		body := []byte(nil)
		if h.Typeflag == tar.TypeReg {
			body = []byte("x\n")
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if body != nil {
			tw.Write(body)
		}
	}
	tw.Close()
	return buf.Bytes()
}

func TestReleaseSpriteStopBringsBackOnlyASafeBack(t *testing.T) {
	stop, err := filepath.Abs("../.satelle/skills/satelle-release-node/scripts/stop.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := func(name string) tar.Header { return tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755} }
	reg := func(name string) tar.Header { return tar.Header{Name: name, Typeflag: tar.TypeReg} }
	cases := map[string][]byte{
		"symlink":  spriteArchive(t, dir("back/"), tar.Header{Name: "back/result", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}),
		"hardlink": spriteArchive(t, dir("back/"), reg("back/a"), tar.Header{Name: "back/result", Typeflag: tar.TypeLink, Linkname: "back/a"}),
		"dotdot":   spriteArchive(t, dir("back/"), reg("back/../x")),
		"absolute": spriteArchive(t, reg("/abs")),
		"outside":  spriteArchive(t, reg("out/log")),
		"ok":       spriteArchive(t, dir("back/"), reg("back/result")),
		"fails":    nil,
	}
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			root, bin, out := realDir(t, t.TempDir()), realDir(t, t.TempDir()), realDir(t, t.TempDir())
			writeFile(t, filepath.Join(bin, "sprite"), strings.ReplaceAll(fakeSprite, "@ROOT@", root), 0o755)
			sn := "satelle-nod-000000000001"
			if err := os.MkdirAll(filepath.Join(root, sn), 0o755); err != nil {
				t.Fatal(err)
			}
			if archive == nil {
				writeFile(t, filepath.Join(root, "fail-back"), "", 0o644)
			} else {
				writeFile(t, filepath.Join(root, "back.tar"), string(archive), 0o644)
			}
			cmd := exec.Command("sh", stop, "nod_000000000001")
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "SATELLE_RELEASE_OUT="+out)
			got, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("stop.sh: %v\n%s", err, got)
			}
			log, _ := os.ReadFile(filepath.Join(root, "log"))
			if !strings.Contains(string(log), "exec -s "+sn+" -- sudo tar -C /release -cf - back\n") || !strings.Contains(string(log), "destroyed "+sn+"\n") {
				t.Fatalf("the fake sprite's log:\n%s", log)
			}
			for _, l := range strings.Split(string(log), "\n") {
				if strings.HasPrefix(l, "exec ") && l != "exec -s "+sn+" -- sudo tar -C /release -cf - back" {
					t.Fatalf("stop.sh asked the node for more than back: %s", l)
				}
			}
			landed, _ := os.ReadDir(out)
			if name == "ok" {
				if b, err := os.ReadFile(filepath.Join(out, "nod_000000000001", "back", "result")); err != nil || string(b) != "x\n" {
					t.Fatalf("a safe back did not land: %v %q\n%s", err, b, got)
				}
				return
			}
			if len(landed) != 0 {
				t.Fatalf("%s: something landed under SATELLE_RELEASE_OUT: %v\n%s", name, landed, got)
			}
		})
	}
}

func TestReleaseSpriteVerifyRefusesADraft(t *testing.T) {
	x := newSpriteBox(t)
	x.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 8 }\n"})
	run, _, fire := x.fireNode()
	if fire.code != 0 {
		t.Fatalf("fire: exit %d\n%s%s", fire.code, fire.out, fire.err)
	}
	if x.draft("v1.0.1") || x.draft("serve-v1.0.1") {
		t.Fatal("the node concluded with a release still a draft")
	}
	writeFile(t, filepath.Join(x.fake, "drafts", "serve-v1.0.1"), "", 0o644)
	read := x.block("\n### Read the node's result\n")
	if read.code == 0 || !strings.Contains(read.out, "verdict accept") || !strings.Contains(read.out, "verify: release serve-v1.0.1 is still a draft\n") || strings.Contains(read.out, "verify: ok") {
		t.Fatalf("read on an accepted result whose release is a draft: exit %d\n%s%s", read.code, read.out, read.err)
	}
	if strings.Contains(read.out, "verify: release v1.0.1 ") {
		t.Fatalf("verify refused the published v1.0.1:\n%s", read.out)
	}
	if r := x.run("execution", "attachment", run, "release-node"); r.code == 0 {
		t.Fatalf("release-node was attached for a run whose release is a draft:\n%s", r.out)
	}
}

func TestReleaseSpriteStopAtReleaseIsToPublishAndAReFirePublishes(t *testing.T) {
	x := newSpriteBox(t)
	id, _ := x.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 9 }\n"})
	oldMain := x.rev("origin/main")
	writeFile(t, filepath.Join(x.fake, "fail-edit"), "", 0o644)
	run, cand, fire, read := x.release()
	if fire.code == 0 || !strings.Contains(fire.out, "failed step release: ") {
		t.Fatalf("fire: exit %d, want the stop at release\n%s%s", fire.code, fire.out, fire.err)
	}
	x.git("fetch", "-q", "origin", "--tags")
	main := x.rev("origin/main")
	if main == oldMain || x.rev(main+"^") != cand || x.rev("v1.0.1^{commit}") != main {
		t.Fatalf("origin/main %s is not the bump on the candidate %s", main, cand)
	}
	for _, want := range []string{"verdict reject\n", "to-publish v1.0.1: main holds " + main + " ", "to-publish serve-v1.0.1: main holds " + main + " ", "verify: ok\n"} {
		if !strings.Contains(read.out, want) {
			t.Fatalf("read lacks %q:\n%s%s", want, read.out, read.err)
		}
	}
	if strings.Contains(read.out, "not the run's base") {
		t.Fatalf("verify required main at the run's base on the to-publish stop:\n%s", read.out)
	}
	if !x.draft("v1.0.1") || !x.draft("serve-v1.0.1") {
		t.Fatal("a release was published by the stopped run")
	}

	x.must("execution", "set", run, "--status", "cancelled")
	x.merged(id)
	if err := os.Remove(filepath.Join(x.fake, "fail-edit")); err != nil {
		t.Fatal(err)
	}
	run2, cand2, fire, read := x.release()
	if fire.code != 0 || read.code != 0 {
		t.Fatalf("re-fire: exit %d\n%s%s\nread: exit %d\n%s%s", fire.code, fire.out, fire.err, read.code, read.out, read.err)
	}
	if cand2 != main {
		t.Fatalf("the re-fire's candidate %s, want origin/main %s", cand2, main)
	}
	rn := x.must("execution", "attachment", run2, "release-node")
	for _, want := range []string{"version 1.0.1\n", "published v1.0.1\n", "published serve-v1.0.1\n", "main-after " + main + "\n", "verify: ok"} {
		if !strings.Contains(rn, want) {
			t.Fatalf("the re-fire's release-node lacks %q:\n%s", want, rn)
		}
	}
	if x.draft("v1.0.1") || x.draft("serve-v1.0.1") || x.latest() != "v1.0.1" {
		t.Fatalf("the re-fire left a draft, or v1.0.1 is not latest (%q)", x.latest())
	}
	x.mainStays(t, main)
}
