package scan

import (
	"strings"
	"testing"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

func TestCursor(t *testing.T) {
	l := newLayout(t)
	l.place(t, "cursor/transcript.jsonl", ".cursor/projects/work-app/agent-transcripts/5d1e/5d1e.jsonl")
	l.place(t, "cursor/elsewhere.jsonl", ".cursor/projects/work-other/agent-transcripts/9a2b/9a2b.jsonl")
	l.place(t, "cursor/elsewhere.jsonl", ".cursor/projects/work-other/not-transcripts/ignored.jsonl")
	st, events, _ := collect(t, Cursor{}, l, env(nil))
	if st.State != "found" || st.Detail != "2 files" {
		t.Fatalf("status %+v", st)
	}
	// The format records no exit status: nothing may become an entry.
	checkContract(t, "cursor", l, events, contract{paired: ""})

	var shells, edit int
	for _, e := range events {
		if e.Kind == model.KindShell {
			shells++
			if e.Exit != nil {
				t.Errorf("exit status invented: %+v", e)
			}
		}
		if e.Kind == model.KindEdit {
			if strings.HasSuffix(e.Path, "refused.ts") {
				t.Errorf("failed edit kept: %+v", e)
			}
			if strings.HasSuffix(e.Path, "src/auth.ts") {
				edit++
			}
		}
	}
	if shells != 4 || edit != 1 {
		t.Errorf("shells=%d edits=%d\n%s", shells, edit, dump(events))
	}
}

func TestCursorUnrecognized(t *testing.T) {
	l := newLayout(t)
	p := l.place(t, "codex/rollout.jsonl", ".cursor/projects/x/agent-transcripts/a.jsonl")
	if _, err := (Cursor{}).Parse(Source{Agent: "cursor", Path: p, Kind: "file"}, l.Root, time.Time{}); err == nil || !strings.Contains(err.Error(), ErrUnrecognized.Error()) {
		t.Fatalf("err = %v", err)
	}
}

func TestCursorHomeOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "cursor/transcript.jsonl", "ch/projects/p/agent-transcripts/s/s.jsonl")
	l.place(t, "cursor/elsewhere.jsonl", ".cursor/projects/p/agent-transcripts/s/s.jsonl")
	_, srcs := Cursor{}.Discover(l.Home, env(map[string]string{"CURSOR_HOME": l.Home + "/ch"}))
	if len(srcs) != 1 {
		t.Fatalf("CURSOR_HOME should replace the default: %v", srcs)
	}
}
