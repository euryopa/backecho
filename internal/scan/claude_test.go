package scan

import "testing"

func TestClaude(t *testing.T) {
	l := newLayout(t)
	l.place(t, "claude/session.jsonl", ".claude/projects/-work-app/7f1c2a90-1111.jsonl")
	l.place(t, "claude/elsewhere.jsonl", ".claude/projects/-work-other/7f1c2a90-2222.jsonl")
	l.place(t, "claude/interrupted.jsonl", ".config/claude/projects/-work-app/7f1c2a90-3333.jsonl")
	st, events, _ := collect(t, Claude{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	checkContract(t, "claude", l, events, contract{paired: "npm test", after: "src/auth.ts"})

	var vet, interrupted bool
	for _, e := range events {
		if e.Command == "go vet ./..." && e.Exit != nil && *e.Exit == 0 {
			vet = true
		}
		if e.Command == "cargo test -p api" && e.Exit != nil {
			interrupted = true
		}
		if e.Command == "npm run test:watch" {
			t.Errorf("background command emitted: %+v", e)
		}
	}
	if !vet {
		t.Error("explicit exitCode not read")
	}
	if interrupted {
		t.Error("interrupted command got an exit status")
	}
}

func TestClaudeConfigDirOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "claude/session.jsonl", "custom/projects/x/s.jsonl")
	l.place(t, "claude/elsewhere.jsonl", ".claude/projects/x/ignored.jsonl")
	_, srcs := Claude{}.Discover(l.Home, env(map[string]string{"CLAUDE_CONFIG_DIR": l.Home + "/custom"}))
	if len(srcs) != 1 {
		t.Fatalf("CLAUDE_CONFIG_DIR should replace both defaults: %v", srcs)
	}
}
