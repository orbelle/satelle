package forge_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/forge"
	"github.com/bobmcallan/satelle/internal/forge/github"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
)

const (
	relRepo  = "orbelle/satelle"
	relToken = "ghs_release"
	relPR    = 20
)

// sha is a 40-character commit id that starts with name.
func sha(name string) string { return name + strings.Repeat("0", 40-len(name)) }

// release is a fake GitHub holding a batch release: base b0; story sty_a's PR
// #11 on epic/sty_a at a1 (x/a.go) and story sty_c's PR #12 on feat/sty_c at c1
// (y/c.go), each approved on its head; the release head release/0.1.0 merges a1
// (m1) then c1 (m2) and bumps the version (v1), as PR #20 whose body lists both.
type release struct {
	gh   *devtest.GitHub
	head string
}

func newRelease(t *testing.T) *release {
	t.Helper()
	gh := devtest.NewGitHub(t)
	gh.Mint(relToken, relRepo)
	gh.AddCommit(relRepo, sha("b0"), nil, "README.md")
	gh.AddCommit(relRepo, sha("a1"), []string{sha("b0")}, "x/a.go")
	gh.AddCommit(relRepo, sha("c1"), []string{sha("b0")}, "y/c.go")
	gh.AddCommit(relRepo, sha("m1"), []string{sha("b0"), sha("a1")}, "x/a.go")
	gh.AddCommit(relRepo, sha("m2"), []string{sha("m1"), sha("c1")}, "y/c.go")
	gh.AddCommit(relRepo, sha("v1"), []string{sha("m2")}, ".version", "CHANGELOG.md", "internal/verb/embedded/CHANGELOG.md")
	gh.AddPR(devtest.PR{Repo: relRepo, Number: 11, HeadRef: "epic/sty_a", HeadSHA: sha("a1"), BaseSHA: sha("b0")})
	gh.AddPR(devtest.PR{Repo: relRepo, Number: 12, HeadRef: "feat/sty_c", HeadSHA: sha("c1"), BaseSHA: sha("b0")})
	gh.SeedStatus(relRepo, sha("a1"), config.DefaultForgeStatusContext, forge.StatusSuccess)
	gh.SeedStatus(relRepo, sha("c1"), config.DefaultForgeStatusContext, forge.StatusSuccess)
	r := &release{gh: gh}
	r.setHead(sha("v1"))
	return r
}

// setHead serves the release PR at head with the two stories' body.
func (r *release) setHead(head string) {
	r.head = head
	r.gh.AddPR(devtest.PR{Repo: relRepo, Number: relPR, HeadRef: "release/0.1.0", HeadSHA: head, BaseSHA: sha("b0"), Body: relBody("sty_a sty_c", 11, 12)})
}

// relBody is a release PR body listing stories, then the URLs of PRs nums.
func relBody(stories string, nums ...int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Release 0.1.0\n\nStories: %s\n", stories)
	for _, n := range nums {
		fmt.Fprintf(&b, "- https://github.com/%s/pull/%d\n", relRepo, n)
	}
	return b.String()
}

func (r *release) approval(t *testing.T) forge.ReleaseApproval {
	t.Helper()
	f, err := forge.Open(github.Name, forge.Config{APIURL: r.gh.URL, Token: relToken, HTTP: r.gh.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return forge.ReleaseApproval{Forge: f, Repo: relRepo, Number: relPR,
		ReleaseHeads: config.DefaultForgeReleaseHeads, PRHeads: config.DefaultForgePRHeads,
		ReleaseFiles: config.DefaultForgeReleaseFiles, StatusContext: config.DefaultForgeStatusContext}
}

func (r *release) run(t *testing.T, a forge.ReleaseApproval) forge.Result {
	t.Helper()
	res, err := forge.RunApproval(context.Background(), a, "")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// rejects runs the approval and wants a reject whose findings carry every one
// of want, and nothing posted.
func (r *release) rejects(t *testing.T, want ...string) {
	t.Helper()
	res := r.run(t, r.approval(t))
	if res.Outcome != verb.OutcomeReject {
		t.Fatalf("outcome = %s (%s), want reject", res.Outcome, res.Findings)
	}
	for _, w := range want {
		if !strings.Contains(res.Findings, w) {
			t.Errorf("findings lack %q:\n%s", w, res.Findings)
		}
	}
	if p := r.gh.Posts(); p != 0 {
		t.Errorf("posts = %d (reviews %+v, statuses %+v), want none", p, r.gh.Reviews(), r.gh.Statuses())
	}
}

// AC5: a release whose stories were each approved on their own heads, merged
// into the release head, plus a commit touching only release files, is
// approved on the exact release head with a success status there.
func TestReleaseApprovalAcceptsAndPostsOnTheExactHead(t *testing.T) {
	r := newRelease(t)
	res := r.run(t, r.approval(t))
	if res.Outcome != verb.OutcomeAccept {
		t.Fatalf("outcome = %s, want accept:\n%s", res.Outcome, res.Findings)
	}
	rv := r.gh.Reviews()
	if len(rv) != 1 || rv[0].Event != "APPROVE" || rv[0].Number != relPR || rv[0].CommitID != r.head {
		t.Errorf("reviews = %+v, want one APPROVE of #%d on %s", rv, relPR, r.head)
	}
	st := r.gh.Statuses()
	if len(st) != 1 || st[0].SHA != r.head || st[0].Context != "satelle/pr-approval" || st[0].State != forge.StatusSuccess {
		t.Errorf("statuses = %+v, want one satelle/pr-approval success on %s", st, r.head)
	}
	if !reflect.DeepEqual(res.Stories, []string{"sty_a", "sty_c"}) {
		t.Errorf("stories = %v", res.Stories)
	}
	rec := res.Record()
	for _, w := range []string{relRepo + "#20 at " + r.head, "Stories: sty_a sty_c", "sty_a: " + relRepo + "#11 at " + sha("a1"), "sty_c: " + relRepo + "#12 at " + sha("c1")} {
		if !strings.Contains(rec, w) {
			t.Errorf("record lacks %q:\n%s", w, rec)
		}
	}
}

// AC1: a PR whose head matches no release head is refused, naming the head.
func TestReleaseApprovalRefusesNonReleaseHead(t *testing.T) {
	r := newRelease(t)
	r.gh.AddPR(devtest.PR{Repo: relRepo, Number: relPR, HeadRef: "feat/x", HeadSHA: r.head, BaseSHA: sha("b0"), Body: relBody("sty_a sty_c", 11, 12)})
	r.rejects(t, "head feat/x", "release/", "not a release PR")
}

// AC2: a listed story with no listed PR on one of its heads is refused, and
// every such story is named.
func TestReleaseApprovalRefusesAStoryWithNoPR(t *testing.T) {
	r := newRelease(t)
	r.gh.SetBody(relRepo, relPR, relBody("sty_a sty_c sty_d", 11))
	r.rejects(t, "sty_c (no listed PR", "sty_d (no listed PR", "epic/sty_d or feat/sty_d")
}

// AC2: a story PR whose head carries no success status under the context, or a
// failure there, is refused naming the story.
func TestReleaseApprovalRefusesAStoryNotApprovedOnItsHead(t *testing.T) {
	r := newRelease(t)
	r.gh.SeedStatus(relRepo, sha("c1"), config.DefaultForgeStatusContext, forge.StatusFailure)
	r.rejects(t, "not approved on their own heads", "sty_c ("+relRepo+"#12 at "+sha("c1"))

	r = newRelease(t)
	r.gh.AddCommit(relRepo, sha("a2"), []string{sha("a1")}, "x/a.go")
	r.gh.AddPR(devtest.PR{Repo: relRepo, Number: 11, HeadRef: "epic/sty_a", HeadSHA: sha("a2"), BaseSHA: sha("b0")})
	r.rejects(t, "sty_a ("+relRepo+"#11 at "+sha("a2")+" has no success satelle/pr-approval status")
}

// AC3: a story PR head that is not an ancestor of the release head is refused
// naming the story.
func TestReleaseApprovalRefusesAStoryHeadNotInTheRelease(t *testing.T) {
	r := newRelease(t)
	r.gh.AddCommit(relRepo, sha("c2"), []string{sha("c1")}, "y/c.go")
	r.gh.AddPR(devtest.PR{Repo: relRepo, Number: 12, HeadRef: "feat/sty_c", HeadSHA: sha("c2"), BaseSHA: sha("b0")})
	r.gh.SeedStatus(relRepo, sha("c2"), config.DefaultForgeStatusContext, forge.StatusSuccess)
	r.rejects(t, "story heads not in the release head", "sty_c (the release head is diverged of its head "+sha("c2"))
}

// AC4: a commit beyond the stories that touches a non-release path, a merge
// joining no story head, a merge that changes a path outside its story's own
// changes, and a range too large to list are each refused, naming the commit
// and the path.
func TestReleaseApprovalRefusesStrayChanges(t *testing.T) {
	t.Run("non-release path", func(t *testing.T) {
		r := newRelease(t)
		r.gh.AddCommit(relRepo, sha("v2"), []string{sha("v1")}, "CHANGELOG.md", "main.go")
		r.setHead(sha("v2"))
		r.rejects(t, "commit "+sha("v2")+" changes main.go, which is not a release file")
	})
	t.Run("merge of no story head", func(t *testing.T) {
		r := newRelease(t)
		r.gh.AddCommit(relRepo, sha("x1"), []string{sha("b0")}, ".version")
		r.gh.AddCommit(relRepo, sha("m3"), []string{sha("v1"), sha("x1")}, ".version")
		r.setHead(sha("m3"))
		r.rejects(t, "merge commit "+sha("m3")+" joins "+sha("x1")+", none of them a story head")
	})
	t.Run("merge outside its story", func(t *testing.T) {
		r := newRelease(t)
		r.gh.AddCommit(relRepo, sha("m2"), []string{sha("m1"), sha("c1")}, "y/c.go", "z/other.go")
		r.rejects(t, "merge commit "+sha("m2")+" of sty_c changes z/other.go, outside sty_c's own changes")
	})
	t.Run("too large", func(t *testing.T) {
		r := newRelease(t)
		many := make([]string, 300)
		for i := range many {
			many[i] = fmt.Sprintf("gen/f%03d.go", i)
		}
		r.gh.AddCommit(relRepo, sha("c1"), []string{sha("b0")}, many...)
		r.rejects(t, "too large to judge mechanically")
	})
}

// headMover moves the release PR's head just before the approval is posted, as
// a push between the judgement and the post does.
type headMover struct {
	forge.Forge
	gh *devtest.GitHub
}

func (m headMover) Approve(ctx context.Context, repo string, n int, sha, body string) (forge.Review, error) {
	m.gh.SetHead(repo, n, strings.Repeat("f", 40))
	return m.Forge.Approve(ctx, repo, n, sha, body)
}

// AC5: a release head that moved since it was judged is refused with head moved
// and gets no status; a self-authored release PR gets the status and a COMMENT.
func TestReleaseApprovalHeadGuardAndSelfAuthoredPR(t *testing.T) {
	r := newRelease(t)
	a := r.approval(t)
	a.Forge = headMover{Forge: a.Forge, gh: r.gh}
	res := r.run(t, a)
	if res.Outcome != verb.OutcomeReject || !strings.Contains(res.Findings, "head moved") {
		t.Errorf("moved head = %s: %s, want reject naming head moved", res.Outcome, res.Findings)
	}
	if r.gh.Posts() != 0 {
		t.Errorf("posts on a moved head = %d, want none", r.gh.Posts())
	}

	r = newRelease(t)
	r.gh.AddPR(devtest.PR{Repo: relRepo, Number: relPR, HeadRef: "release/0.1.0", HeadSHA: r.head, BaseSHA: sha("b0"),
		Body: relBody("sty_a sty_c", 11, 12), Author: r.gh.ReviewerLogin})
	res = r.run(t, r.approval(t))
	if res.Outcome != verb.OutcomeAccept || !strings.Contains(res.Findings, "self-authored") {
		t.Fatalf("self-authored = %s: %s, want accept noting self-authored", res.Outcome, res.Findings)
	}
	if st := r.gh.Statuses(); len(st) != 1 || st[0].SHA != r.head || st[0].State != forge.StatusSuccess {
		t.Errorf("statuses = %+v, want one success on %s", st, r.head)
	}
	if rv := r.gh.Reviews(); len(rv) != 1 || rv[0].Event != "COMMENT" || rv[0].CommitID != r.head {
		t.Errorf("reviews = %+v, want one COMMENT on %s", rv, r.head)
	}
}

// No provider is the release approval's no-op; a provider that could not be
// opened is its reject.
func TestReleaseApprovalWithoutAForge(t *testing.T) {
	res, _ := forge.RunApproval(context.Background(), forge.ReleaseApproval{Repo: relRepo, Number: relPR}, "")
	if res.Outcome != verb.OutcomeNoOp {
		t.Errorf("no forge = %+v, want no-op", res)
	}
	res, _ = forge.RunApproval(context.Background(), forge.ReleaseApproval{OpenErr: errors.New("no token")}, "")
	if res.Outcome != verb.OutcomeReject || !strings.Contains(res.Findings, "no token") {
		t.Errorf("open error = %+v, want reject naming it", res)
	}
}

// AC2: the body's Stories line and the PR URLs after it are read; other URLs
// are ignored.
func TestParseReleaseStories(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		stories    []string
		prs        []forge.PRRef
		err        error
	}{
		{"no line", "Release 1\nhttps://github.com/o/r/pull/1", nil, nil, forge.ErrNoStories},
		{"empty line", "Stories:   \n", nil, nil, forge.ErrNoStories},
		{"spaced and repeated", "  Stories:  sty_a,  sty_b\tsty_a \n\n- https://github.com/o/r/pull/3\n* [#4](https://github.com/o/r/pull/4).",
			[]string{"sty_a", "sty_b"}, []forge.PRRef{{Repo: "o/r", Number: 3}, {Repo: "o/r", Number: 4}}, nil},
		{"other urls and repos", "https://github.com/o/r/pull/9\nStories: sty_a\nsee https://example.com/docs and https://github.com/o/r/issues/5\nhttps://ghe.example.com/acme/x/pull/7/",
			[]string{"sty_a"}, []forge.PRRef{{Repo: "acme/x", Number: 7}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := forge.ParseReleaseStories(tc.body)
			if !errors.Is(err, tc.err) || !reflect.DeepEqual(got.Stories, tc.stories) || !reflect.DeepEqual(got.PRs, tc.prs) {
				t.Errorf("ParseReleaseStories = %+v, %v; want stories %v, PRs %v, err %v", got, err, tc.stories, tc.prs, tc.err)
			}
		})
	}
	for _, bad := range []string{"github.com/o/r/pull/1", "https://github.com/o/r/pull/0", "https://github.com/o/r/pull/01", "https://github.com/o/pull/1", "ftp://h/o/r/pull/1"} {
		if _, _, err := forge.ParsePRURL(bad); err == nil {
			t.Errorf("ParsePRURL(%q) parsed", bad)
		}
	}
}

// A story whose only listed PR is on another repo has no PR in this release.
func TestReleaseApprovalIgnoresPRsOnAnotherRepo(t *testing.T) {
	r := newRelease(t)
	r.gh.SetBody(relRepo, relPR, relBody("sty_a sty_c", 11)+"- https://github.com/acme/other/pull/12\n")
	r.rejects(t, "sty_c (no listed PR on "+relRepo)
}
