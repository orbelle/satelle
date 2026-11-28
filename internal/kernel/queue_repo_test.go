package kernel

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

func twoRepos() reporeg.Registry {
	return reporeg.Registry{Tracked: map[string]reporeg.Entry{
		"satelle":        {URL: "https://github.com/o/satelle"},
		"satelle-server": {URL: "https://github.com/o/satelle-server"},
	}}
}

func writeTwoRepos(t *testing.T, root string) {
	t.Helper()
	write(t, filepath.Join(root, reporeg.TrackedFile), "[repos.satelle]\norigin = \"https://github.com/o/satelle\"\n[repos.satelle-server]\norigin = \"https://github.com/o/satelle-server\"\n")
}

func repoStory(t *testing.T, k *Kernel, title, repo string) item.Item {
	t.Helper()
	f := item.Fields{Kind: ptr("story"), Title: ptr(title)}
	if repo != "" {
		f.Repo = ptr(repo)
	}
	return mustCreate(t, k, f)
}

func TestQueueRepoFilterMembershipAndFire(t *testing.T) {
	root, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	k.Close()
	writeTwoRepos(t, root)
	queueFile(t, root, "release", `title = "Release"
[filter]
kind = "story"
status = "backlog"
repo = "satelle-server"
[trigger]
task = "`+task.ID+`"
`)
	k = open()
	srv := repoStory(t, k, "server", "satelle-server")
	sat := repoStory(t, k, "satelle", "satelle")
	self := repoStory(t, k, "self", "")
	srvAdded := repoStory(t, k, "server added", "satelle-server")
	if err := k.Cache.Sync(k.Ledger); err != nil {
		t.Fatal(err)
	}
	qid, _ := k.ResolveQueue("release")
	before := chainLen(t, k, qid)
	out, err := k.Enqueue("release", []string{sat.ID, srvAdded.ID}, "default:x->backlog", "me")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Outside, []string{sat.ID}) || !slices.Equal(out.Added, []string{srvAdded.ID}) || out.Repo != "satelle-server" {
		t.Fatalf("enqueue %+v", out)
	}
	if n := chainLen(t, k, qid); n != before+1 {
		t.Fatalf("chain grew by %d, want one queue.added", n-before)
	}
	_, _, ms, err := k.QueueMembers("release")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{srvAdded.ID, srv.ID}
	if !slices.Equal(ms.Members, want) || slices.Contains(ms.Members, self.ID) {
		t.Fatalf("members %v, want %v", ms.Members, want)
	}
	fire, err := k.Fire("release", "", "by hand", "me")
	if err != nil || fire.Run == "" {
		t.Fatalf("fire %+v %v", fire, err)
	}
	if rec := runRecord(t, k, fire.Run); !slices.Equal(rec.Inputs, want) {
		t.Fatalf("fired inputs %v, want %v", rec.Inputs, want)
	}
}

func TestQueueNoRepoFilterUnchanged(t *testing.T) {
	for _, multi := range []bool{false, true} {
		name := "n=1"
		if multi {
			name = "multi-repo"
		}
		t.Run(name, func(t *testing.T) {
			root, open := queueKernel(t)
			k := open()
			task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
			k.Close()
			queueFile(t, root, "release", "title = \"Release\"\n[filter]\nkind = \"story\"\n[trigger]\ntask = \""+task.ID+"\"\n")
			k = open()
			want := []string{repoStory(t, k, "self", "").ID}
			if multi {
				k.Repos = twoRepos()
				want = append(want, repoStory(t, k, "server", "satelle-server").ID)
			}
			_, _, ms, err := k.QueueMembers("release")
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(ms.Members)
			slices.Sort(want)
			if !slices.Equal(ms.Members, want) {
				t.Fatalf("members %v, want %v", ms.Members, want)
			}
			fire, err := k.Fire("release", "", "by hand", "me")
			if err != nil || fire.Run == "" {
				t.Fatalf("fire %+v %v", fire, err)
			}
			got := runRecord(t, k, fire.Run).Inputs
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("fired %v, want %v", got, want)
			}
		})
	}
}
