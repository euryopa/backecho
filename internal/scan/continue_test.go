package scan

import "testing"

func TestContinue(t *testing.T) {
	l := newLayout(t)
	l.place(t, "continue/session.json", ".continue/sessions/5b1f2c3d-0000-4000-8000-000000000001.json")
	l.place(t, "continue/cli.json", ".continue/sessions/5b1f2c3d-0000-4000-8000-000000000002.json")
	l.place(t, "continue/elsewhere.json", ".continue/sessions/5b1f2c3d-0000-4000-8000-000000000003.json")
	l.place(t, "continue/sessions.json", ".continue/sessions/sessions.json")
	st, events, unrec := collect(t, Continue{}, l, env(nil))
	if st.State != "found" || unrec != 0 {
		t.Fatalf("status %+v unrecognized=%d", st, unrec)
	}
	// Continue formats an exit code only for a failure.
	checkContract(t, "continue", l, events, contract{paired: ""})

	got := map[string]bool{}
	for _, e := range events {
		if e.Cwd != l.Root {
			t.Errorf("cwd = %q, want %q", e.Cwd, l.Root)
		}
		switch {
		case e.Command == "npm test" && e.Exit != nil:
			got["ide-fail"] = *e.Exit == 1
		case e.Command == "go test ./..." && e.Exit != nil:
			got["cli-fail"] = *e.Exit == 2
		case e.Kind == "shell" && e.Exit != nil:
			t.Errorf("exit inferred without a failure marker: %+v", e)
		case e.Kind == "edit" && e.Path == "src/missing.ts":
			t.Errorf("errored edit kept: %+v", e)
		case e.Kind == "edit":
			got[e.Path] = true
		}
	}
	for _, k := range []string{"ide-fail", "cli-fail", "src/auth.ts", l.Root + "/pkg/auth.go"} {
		if !got[k] {
			t.Errorf("missing %s:\n%s", k, dump(events))
		}
	}
}

func TestContinueGlobalDirOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "continue/session.json", "cg/sessions/a.json")
	l.place(t, "continue/sessions.json", "cg/sessions/sessions.json")
	l.place(t, "continue/cli.json", ".continue/sessions/b.json")
	_, srcs := Continue{}.Discover(l.Home, env(map[string]string{"CONTINUE_GLOBAL_DIR": l.Home + "/cg"}))
	if len(srcs) != 1 {
		t.Fatalf("got %v", srcs)
	}
}

func TestContinuePath(t *testing.T) {
	if got := continuePath("file:///home/u/my%20app"); got != "/home/u/my app" {
		t.Errorf("got %q", got)
	}
	if got := continuePath("vscode-remote://ssh-remote+h/home/u"); got != "" {
		t.Errorf("got %q", got)
	}
}
