package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// geminiPlace places a fixture and fills {{ROOT_HASH}} with sha256(root),
// which layout.place cannot compute.
func geminiPlace(t *testing.T, l layout, src, dst string) string {
	t.Helper()
	p := l.place(t, src, dst)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.ReplaceAll(string(b), "{{ROOT_HASH}}", geminiHash(l.Root)))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGemini(t *testing.T) {
	l := newLayout(t)
	geminiPlace(t, l, "gemini/session.json", ".gemini/tmp/app/chats/session-2026-10-04T10-00-5f0c2a6e.json")
	geminiPlace(t, l, "gemini/elsewhere.json", ".gemini/tmp/other/chats/session-2026-10-04T11-00-5f0c2a6e.json")
	st, events, _ := collect(t, Gemini{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	for _, e := range events {
		if e.Kind == model.KindEdit && strings.Contains(e.Path, "missing.ts") {
			t.Errorf("failed edit kept: %+v", e)
		}
	}
	checkContract(t, "gemini", l, events, contract{paired: "npm test", after: "src/auth.ts"})
}

// The current .jsonl format: re-appended message records, a $rewindTo, a
// truncated last line, and a success with no "Exit Code" line.
func TestGeminiJSONL(t *testing.T) {
	l := newLayout(t)
	p := geminiPlace(t, l, "gemini/session.jsonl", ".gemini/tmp/app/chats/session-2026-10-04T12-00-7a1e0000.jsonl")
	events, err := Gemini{}.Parse(Source{Agent: "gemini", Path: p, Kind: "file"}, l.Root, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var shells, edits int
	for _, e := range events {
		switch e.Kind {
		case model.KindShell:
			shells++
			if e.Cwd != filepath.Join(l.Root, "pkg") {
				t.Errorf("cwd = %q", e.Cwd)
			}
			if shells == 1 && (e.Exit == nil || *e.Exit != 2) {
				t.Errorf("failure exit: %v", dump([]model.Event{e}))
			}
			if shells == 2 && e.Exit != nil {
				t.Errorf("success without an Exit Code line must be unknown: %v", *e.Exit)
			}
		case model.KindEdit:
			edits++
		}
	}
	if shells != 2 || edits != 1 {
		t.Fatalf("got %d shells, %d edits:\n%s", shells, edits, dump(events))
	}
}

func TestGeminiEnvRoots(t *testing.T) {
	l := newLayout(t)
	geminiPlace(t, l, "gemini/session.json", "gh/.gemini/tmp/x/chats/session-a.json")
	geminiPlace(t, l, "gemini/session.json", "gd/tmp/x/chats/session-b.json")
	_, srcs := Gemini{}.Discover(l.Home, env(map[string]string{"GEMINI_CLI_HOME": l.Home + "/gh"}))
	if len(srcs) != 1 || !strings.Contains(srcs[0].Path, "session-a") {
		t.Fatalf("GEMINI_CLI_HOME: %v", srcs)
	}
	_, srcs = Gemini{}.Discover(l.Home, env(map[string]string{"GEMINI_DATA_DIR": l.Home + "/gd"}))
	if len(srcs) != 1 || !strings.Contains(srcs[0].Path, "session-b") {
		t.Fatalf("GEMINI_DATA_DIR: %v", srcs)
	}
}
