package scan

import (
	"testing"

	"github.com/euryopa/backecho/internal/model"
)

func TestAntigravity(t *testing.T) {
	l := newLayout(t)
	l.place(t, "antigravity/transcript.jsonl", ".gemini/antigravity/brain/1b2c3d4e-0000-4000-8000-000000000001/.system_generated/logs/transcript.jsonl")
	l.place(t, "antigravity/transcript.jsonl", ".gemini/antigravity/brain/1b2c3d4e-0000-4000-8000-000000000001/.system_generated/logs/transcript_full.jsonl")
	l.place(t, "antigravity/elsewhere.jsonl", ".gemini/antigravity-cli/brain/1b2c3d4e-0000-4000-8000-000000000002/.system_generated/logs/transcript.jsonl")
	st, events, _ := collect(t, Antigravity{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	var shells, edits int
	for _, e := range events {
		switch e.Kind {
		case model.KindShell:
			shells++
			if e.Exit != nil {
				t.Errorf("unverified format produced an exit: %+v", e)
			}
		case model.KindEdit:
			edits++
		}
	}
	// One conversation (full copy preferred): npm test x2, make test-unknown, ls -la.
	if shells != 4 || edits != 1 {
		t.Fatalf("shells=%d edits=%d\n%s", shells, edits, dump(events))
	}
	checkContract(t, "antigravity", l, events, contract{paired: ""})
}

func TestAntigravityEnvRoot(t *testing.T) {
	l := newLayout(t)
	l.place(t, "antigravity/transcript.jsonl", "ag/brain/c1/.system_generated/logs/transcript.jsonl")
	_, srcs := Antigravity{}.Discover(l.Home, env(map[string]string{"ANTIGRAVITY_DATA_DIR": l.Home + "/ag"}))
	if len(srcs) != 1 {
		t.Fatalf("got %v", srcs)
	}
}
