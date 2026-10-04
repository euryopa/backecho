package scan

import (
	"strings"
	"testing"

	"github.com/euryopa/backecho/internal/model"
)

func TestQwen(t *testing.T) {
	l := newLayout(t)
	l.place(t, "qwen/session.jsonl", ".qwen/projects/-work-app/chats/9b2d0000-0000-4000-8000-0000000000aa.jsonl")
	l.place(t, "qwen/elsewhere.jsonl", ".qwen/projects/-work-other/chats/9b2d0000-0000-4000-8000-0000000000bb.jsonl")
	st, events, _ := collect(t, Qwen{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	for _, e := range events {
		if e.Kind == model.KindEdit && strings.Contains(e.Path, "denied.py") {
			t.Errorf("failed edit kept: %+v", e)
		}
	}
	checkContract(t, "qwen", l, events, contract{paired: "pytest -k auth", after: "app/auth.py"})
}

// Qwen versions that still wrote Gemini-shaped chats under tmp/.
func TestQwenLegacyGeminiShape(t *testing.T) {
	l := newLayout(t)
	geminiPlace(t, l, "gemini/session.json", ".qwen/tmp/abc/chats/session-1.json")
	_, events, _ := collect(t, Qwen{}, l, env(nil))
	checkContract(t, "qwen", l, events, contract{paired: "npm test", after: "src/auth.ts"})
}

func TestQwenEnvRoot(t *testing.T) {
	l := newLayout(t)
	l.place(t, "qwen/session.jsonl", "qd/projects/x/chats/s.jsonl")
	l.place(t, "qwen/session.jsonl", "qd/projects/x/chats/s.runtime.json")
	_, srcs := Qwen{}.Discover(l.Home, env(map[string]string{"QWEN_DATA_DIR": l.Home + "/qd"}))
	if len(srcs) != 1 {
		t.Fatalf("got %v", srcs)
	}
}
