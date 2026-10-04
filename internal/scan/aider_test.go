package scan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

func TestAider(t *testing.T) {
	l := newLayout(t)
	l.place(t, "aider/home.chat.history.md", ".aider.chat.history.md")
	b, err := os.ReadFile(filepath.Join(testdataDir(t), "aider", "repo.chat.history.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Root, ".aider.chat.history.md"), []byte(l.expand(string(b))), 0o644); err != nil {
		t.Fatal(err)
	}
	st, events, _ := collect(t, Aider{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	// Aider never writes an exit status: commands are seen, none provable.
	checkContract(t, "aider", l, events, contract{paired: ""})

	var tests, edit, suggested int
	for _, e := range events {
		if e.Kind == model.KindShell && e.Exit != nil {
			t.Errorf("aider command got an exit status: %+v", e)
		}
		if e.Cwd != l.Root {
			t.Errorf("repo-local event cwd = %q", e.Cwd)
		}
		switch {
		case e.Command == "npm test":
			tests++
		case e.Command == "ls -la":
			suggested++
		case e.Kind == model.KindEdit && e.Path == "src/auth.ts":
			edit++
		}
	}
	if tests != 2 || edit != 1 || suggested != 1 {
		t.Errorf("tests=%d edit=%d suggested=%d:\n%s", tests, edit, suggested, dump(events))
	}
}

func TestAiderHomeOnlyMatchesNothing(t *testing.T) {
	l := newLayout(t)
	l.place(t, "aider/repo.chat.history.md", ".aider.chat.history.md")
	st, srcs := Aider{}.Discover(l.Home, env(nil))
	if st.State != "found" || len(srcs) != 1 {
		t.Fatalf("status %+v %v", st, srcs)
	}
	evs, err := Aider{}.Parse(srcs[0], l.Root, time.Time{})
	if err != nil || len(evs) != 0 {
		t.Fatalf("home log without cwd matched the repo: %v %v", evs, err)
	}
}
