package scan

import "testing"

func TestCodex(t *testing.T) {
	l := newLayout(t)
	l.place(t, "codex/rollout.jsonl", ".codex/sessions/2026/10/04/rollout-2026-10-04T14-00-00-0199a1b2.jsonl")
	st, events, _ := collect(t, Codex{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	checkContract(t, "codex", l, events, contract{paired: "cargo test -p api", after: "crates/api/src/lib.rs"})
}

func TestCodexHomeOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "codex/rollout.jsonl", "ch/archived_sessions/rollout-x.jsonl")
	_, srcs := Codex{}.Discover(l.Home, env(map[string]string{"CODEX_HOME": l.Home + "/ch"}))
	if len(srcs) != 1 {
		t.Fatalf("got %v", srcs)
	}
}
