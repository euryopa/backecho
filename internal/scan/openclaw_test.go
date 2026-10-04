package scan

import (
	"path/filepath"
	"testing"
)

func TestOpenClaw(t *testing.T) {
	l := newLayout(t)
	l.place(t, "openclaw/session.jsonl", ".openclaw/agents/main/sessions/oc-session-0001.jsonl")
	l.place(t, "openclaw/elsewhere.jsonl", ".openclaw/agents/work/sessions/oc-session-0002.jsonl")
	l.place(t, "openclaw/sessions.json", ".openclaw/agents/main/sessions/sessions.json")
	st, events, _ := collect(t, OpenClaw{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	checkContract(t, "openclaw", l, events, contract{paired: "pytest -k auth", after: "src/auth.py"})
	for _, e := range events {
		if (e.Command == "make test-unknown" || e.Command == "npm run build") && e.Exit != nil {
			t.Errorf("exec without a final exitCode got a status: %+v", e)
		}
	}
}

func TestOpenClawStateDirOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "openclaw/session.jsonl", "state/agents/main/sessions/a.jsonl")
	l.place(t, "openclaw/elsewhere.jsonl", ".openclaw/agents/main/sessions/ignored.jsonl")
	_, srcs := OpenClaw{}.Discover(l.Home, env(map[string]string{"OPENCLAW_STATE_DIR": filepath.Join(l.Home, "state")}))
	if len(srcs) != 1 {
		t.Fatalf("OPENCLAW_STATE_DIR should replace the default: %v", srcs)
	}
}
