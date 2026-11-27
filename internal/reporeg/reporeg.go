package reporeg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

const (
	TrackedFile   = "repos.toml"
	LocalFile     = "repos.local.toml"
	Self          = "self"
	TierStandard  = "standard"
	TierProtected = "protected"

	Unparseable = "<unparseable origin>"
)

type Entry struct {
	URL   string `toml:"origin"`
	Base  string `toml:"base,omitempty"`
	Fetch string `toml:"fetch,omitempty"`
	Tier  string `toml:"tier,omitempty"`
}

func (e Entry) EffectiveTier() string {
	if e.Tier == "" {
		return TierStandard
	}
	return e.Tier
}

type Local struct {
	Path string `toml:"path"`
}

type Registry struct {
	Tracked map[string]Entry
	Local   map[string]Local
}

var nameRule = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func ValidName(name string) bool { return nameRule.MatchString(name) }

func entryRef(name string) string {
	if ValidName(name) {
		return "repos." + name
	}
	return "repos." + strconv.Quote(name)
}

func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' '
}

func HasUnsafe(s string) bool { return strings.IndexFunc(s, unsafeRune) >= 0 }

func CheckName(name string) error {
	switch {
	case name == Self:
		return fmt.Errorf("%s: %s: self is the control repository itself and is never declared", TrackedFile, entryRef(name))
	case !ValidName(name):
		return fmt.Errorf("%s: %s: a name is lower-case letters, digits, '.', '_' and '-', starting with a letter or digit", TrackedFile, entryRef(name))
	}
	return nil
}

func CheckTier(name, tier string) error {
	if tier == "" || tier == TierStandard || tier == TierProtected {
		return nil
	}
	return fmt.Errorf("%s: %s: tier is not %s or %s", TrackedFile, entryRef(name), TierStandard, TierProtected)
}

func CheckEntry(name string, e Entry) []error {
	if err := CheckName(name); err != nil {
		return []error{err}
	}
	var errs []error
	if err := CheckOrigin(name, e.URL); err != nil {
		errs = append(errs, err)
	}
	for _, f := range []struct{ key, v string }{{"base", e.Base}, {"fetch", e.Fetch}} {
		if HasUnsafe(f.v) {
			errs = append(errs, fmt.Errorf("%s: %s: %s holds a control or format character", TrackedFile, entryRef(name), f.key))
		}
	}
	if err := CheckTier(name, e.Tier); err != nil {
		errs = append(errs, err)
	}
	return errs
}

func pathOK(p string) bool { return p != "" && filepath.IsAbs(p) && !HasUnsafe(p) }

func (r Registry) localProblems(name string, l Local) []error {
	ref := entryRef(name)
	var errs []error
	if _, ok := r.Tracked[name]; !ok {
		errs = append(errs, fmt.Errorf("%s: %s: %s declares no repository of that name", LocalFile, ref, TrackedFile))
	}
	switch {
	case l.Path == "":
		errs = append(errs, fmt.Errorf("%s: %s: path is required", LocalFile, ref))
	case HasUnsafe(l.Path):
		errs = append(errs, fmt.Errorf("%s: %s: path holds a control or format character", LocalFile, ref))
	case !filepath.IsAbs(l.Path):
		errs = append(errs, fmt.Errorf("%s: %s: path is not absolute", LocalFile, ref))
	}
	return errs
}

func (r Registry) Problems() []error {
	var errs []error
	for _, name := range sortedKeys(r.Tracked) {
		errs = append(errs, CheckEntry(name, r.Tracked[name])...)
	}
	for _, name := range sortedKeys(r.Local) {
		errs = append(errs, r.localProblems(name, r.Local[name])...)
	}
	return errs
}

func (r Registry) EntryOK(name string) bool {
	e, ok := r.Tracked[name]
	return ok && len(CheckEntry(name, e)) == 0
}

func (r Registry) LocalPath(name string) (path string, mapped, ok bool) {
	l, mapped := r.Local[name]
	if !mapped {
		return "", false, false
	}
	return l.Path, true, len(r.localProblems(name, l)) == 0
}

func (r Registry) Targets() []string {
	out := []string{Self}
	for _, n := range sortedKeys(r.Tracked) {
		if n != Self {
			out = append(out, n)
		}
	}
	return out
}

func (r Registry) Declared() int {
	n := len(r.Tracked)
	if _, ok := r.Tracked[Self]; ok {
		n--
	}
	return n
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func Exists(dataDir string) bool {
	for _, f := range []string{TrackedFile, LocalFile} {
		if _, err := os.Lstat(filepath.Join(dataDir, f)); err == nil {
			return true
		}
	}
	return false
}

func Load(dataDir string) (Registry, error) {
	reg := Registry{Tracked: map[string]Entry{}, Local: map[string]Local{}}
	var errs []error
	var tracked struct {
		Repos map[string]Entry `toml:"repos"`
	}
	if found, err := decode(dataDir, TrackedFile, &tracked); err != nil {
		errs = append(errs, err)
	} else if found && tracked.Repos != nil {
		reg.Tracked = tracked.Repos
	}
	var local struct {
		Repos map[string]Local `toml:"repos"`
	}
	if found, err := decode(dataDir, LocalFile, &local); err != nil {
		errs = append(errs, err)
	} else if found && local.Repos != nil {
		reg.Local = local.Repos
	}
	return reg, errors.Join(errs...)
}

func LoadLocalQuiet(dataDir string) (map[string]string, bool) {
	var local struct {
		Repos map[string]Local `toml:"repos"`
	}
	if _, err := decode(dataDir, LocalFile, &local); err != nil {
		return nil, false
	}
	out := map[string]string{}
	for name, l := range local.Repos {
		if ValidName(name) && name != Self && pathOK(l.Path) {
			out[name] = l.Path
		}
	}
	return out, true
}

func decode(dataDir, file string, v any) (bool, error) {
	data, err := readRegular(filepath.Join(dataDir, file))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("%s: %w", file, err)
	}
	md, err := toml.Decode(string(data), v)
	if err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) && pe.Position.Line > 0 {
			return true, fmt.Errorf("%s: not valid TOML at line %d", file, pe.Position.Line)
		}
		return true, fmt.Errorf("%s: not valid TOML or a value of the wrong type", file)
	}
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, len(un))
		for i, k := range un {
			parts := make([]string, len(k))
			for j, p := range k {
				parts[j] = strconv.Quote(p)
			}
			keys[i] = strings.Join(parts, ".")
		}
		return true, fmt.Errorf("%s: unknown key %s", file, strings.Join(keys, ", "))
	}
	return true, nil
}

func readRegular(path string) ([]byte, error) {
	linfo, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, errors.New("cannot be read")
	}
	if !linfo.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot be read")
	}
	defer f.Close()
	finfo, err := f.Stat()
	if err != nil || !os.SameFile(linfo, finfo) || !finfo.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, errors.New("cannot be read")
	}
	return b, nil
}

func Write(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func render(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := toml.NewEncoder(&b)
	enc.Indent = ""
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func RenderTracked(m map[string]Entry) ([]byte, error) {
	return render(struct {
		Repos map[string]Entry `toml:"repos"`
	}{m})
}

func RenderLocal(m map[string]Local) ([]byte, error) {
	return render(struct {
		Repos map[string]Local `toml:"repos"`
	}{m})
}

const (
	userClass = `[A-Za-z0-9_][A-Za-z0-9._-]*`
	hostClass = `[A-Za-z0-9][A-Za-z0-9.-]*`
	segsClass = `[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)+/?`
)

var (
	plainUserRE = regexp.MustCompile(`^` + userClass + `$`)
	originRE    = regexp.MustCompile(`^(?:` +
		`https?://` + hostClass + `(?::[0-9]+)?/` + segsClass +
		`|ssh://(?:` + userClass + `@)?` + hostClass + `(?::[0-9]+)?/` + segsClass +
		`|(?:` + userClass + `@)?` + hostClass + `:` + segsClass +
		`)$`)
)

var ErrUnreadableOrigin = errors.New("origin is not a plain ssh, scp-style or http(s) repository URL")

func plainUser(s string) bool { return plainUserRE.MatchString(s) }

func CheckOrigin(name, origin string) error {
	if originRE.MatchString(origin) {
		return nil
	}
	if _, ok := reduce(origin); ok {
		return fmt.Errorf("%s: %s: origin carries credentials, a query or a fragment; register the plain repository URL", TrackedFile, entryRef(name))
	}
	return fmt.Errorf("%s: %s: origin is not a plain ssh, scp-style or http(s) repository URL", TrackedFile, entryRef(name))
}

func reduce(s string) (string, bool) {
	if originRE.MatchString(s) {
		return s, true
	}
	r, _, _, ok := reduceParts(s)
	return r, ok
}

func reduceParts(s string) (candidate, host, path string, ok bool) {
	var prefix, authority, rest string
	if scheme, after, found := strings.Cut(s, "://"); found {
		switch scheme {
		case "http", "https", "ssh":
		default:
			return "", "", "", false
		}
		prefix = scheme + "://"
		end := strings.IndexAny(after, "/?#")
		if end < 0 {
			end = len(after)
		}
		authority, rest = after[:end], after[end:]
	} else {
		end := strings.IndexAny(s, ":/?#")
		if end < 0 || s[end] != ':' {
			return "", "", "", false
		}
		authority, rest = s[:end], s[end:]
	}
	if strings.Count(authority, "@") > 1 || strings.Contains(rest, "@") {
		return "", "", "", false
	}
	user, hostPort, hasUser := strings.Cut(authority, "@")
	if !hasUser {
		hostPort = authority
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	candidate = prefix
	if hasUser && (prefix == "" || prefix == "ssh://") && plainUser(user) {
		candidate += user + "@"
	}
	candidate += hostPort + rest
	if !originRE.MatchString(candidate) {
		return "", "", "", false
	}
	host, _, _ = strings.Cut(hostPort, ":")
	return candidate, host, rest[1:], true
}

func DisplayOrigin(s string) string {
	if r, ok := reduce(s); ok {
		return r
	}
	return Unparseable
}

func NormaliseOrigin(s string) (string, error) {
	_, host, path, ok := reduceParts(s)
	if !ok {
		return "", ErrUnreadableOrigin
	}
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	return strings.ToLower(host) + "/" + path, nil
}
