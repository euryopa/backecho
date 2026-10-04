package scan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func crushFixture(t *testing.T, l layout) {
	t.Helper()
	dbPath := filepath.Join(l.Root, ".crush", "crush.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	n := 0
	msg := func(session, role string, sec int, parts ...any) string {
		n++
		return fmt.Sprintf(`INSERT INTO messages (id, session_id, role, parts, created_at, updated_at) VALUES ('m%d', %s, %s, %s, %d, %d)`,
			n, ocSQL(session), ocSQL(role), ocSQL(js(parts)), ocT0.Unix()+int64(sec), ocT0.Unix()+int64(sec))
	}
	call := func(id, name string, input map[string]any) any {
		return map[string]any{"type": "tool_call", "data": map[string]any{"id": id, "name": name, "input": js(input), "finished": true}}
	}
	result := func(id, name, content string, meta map[string]any, isErr bool) any {
		m := ""
		if meta != nil {
			m = js(meta)
		}
		return map[string]any{"type": "tool_result", "data": map[string]any{"tool_call_id": id, "name": name, "content": content, "metadata": m, "is_error": isErr}}
	}
	bang := func(cmd, out string, exit int) any {
		return map[string]any{"type": "shell_command", "data": map[string]any{"command": cmd, "output": out, "exit_code": exit}}
	}
	root, elsewhere := l.Root, l.expand("{{ELSEWHERE}}")
	failOut := "FAIL src/auth.test.ts\nAuthorization: Bearer SECRETTOKEN123\nExit code 1"
	buildDB(t, dbPath,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, parent_session_id TEXT, title TEXT NOT NULL, message_count INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, role TEXT NOT NULL, parts TEXT NOT NULL default '[]', model TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, finished_at INTEGER)`,
		`INSERT INTO sessions (id, title, updated_at, created_at) VALUES ('s1', 'auth', 0, 0), ('s2', 'other', 0, 0)`,
		msg("s1", "assistant", 10, call("t1", "bash", map[string]any{"command": "npm test", "description": "run tests"})),
		msg("s1", "tool", 12, result("t1", "bash", failOut+"\n\n<cwd>"+root+"</cwd>", map[string]any{"start_time": 1, "end_time": 2, "output": failOut, "working_directory": root}, false)),
		msg("s1", "assistant", 60, call("t2", "edit", map[string]any{"file_path": root + "/src/auth.ts", "old_string": "a", "new_string": "b"})),
		msg("s1", "tool", 61, result("t2", "edit", "ok", nil, false)),
		msg("s1", "assistant", 70, call("t2b", "write", map[string]any{"file_path": root + "/src/failed.ts", "content": "x"})),
		msg("s1", "tool", 71, result("t2b", "write", "permission denied", nil, true)),
		msg("s1", "user", 120, bang("npm test", "12 passing", 0)),
		msg("s1", "assistant", 130, call("t3", "bash", map[string]any{"command": "make test-unknown"})),
		msg("s1", "tool", 131, result("t3", "bash", "ok", map[string]any{"output": "ok", "working_directory": root}, false)),
		msg("s1", "user", 140, bang("ls -la", "total 0", 0)),
		msg("s2", "assistant", 150, call("t4", "bash", map[string]any{"command": "make test-elsewhere"})),
		msg("s2", "tool", 151, result("t4", "bash", "boom", map[string]any{"output": "boom\nExit code 2", "working_directory": elsewhere}, false)),
	)
}

func TestCrush(t *testing.T) {
	l := newLayout(t)
	crushFixture(t, l)
	st, events, _ := collect(t, Crush{}, l, env(nil))
	if st.State != "empty" {
		t.Fatalf("status %+v", st)
	}
	var sawFail bool
	for _, e := range events {
		if strings.Contains(e.Path, "failed.ts") {
			t.Errorf("errored edit kept: %+v", e)
		}
		if e.Command == "make test-unknown" && e.Exit != nil {
			t.Errorf("exit 0 inferred without a marker: %+v", e)
		}
		if e.Command == "npm test" && e.Exit != nil && *e.Exit == 1 && e.Cwd == l.Root {
			sawFail = true
		}
	}
	if !sawFail {
		t.Errorf("bash failure marker not read:\n%s", dump(events))
	}
	checkContract(t, "crush", l, events, contract{paired: "npm test", after: "src/auth.ts"})
}

func TestCrushDiscoverRepo(t *testing.T) {
	l := newLayout(t)
	if srcs := (Crush{}).DiscoverRepo(l.Root); len(srcs) != 0 {
		t.Fatalf("got %v", srcs)
	}
	crushFixture(t, l)
	srcs := Crush{}.DiscoverRepo(l.Root)
	if len(srcs) != 1 || srcs[0].Kind != "sqlite" {
		t.Fatalf("got %v", srcs)
	}
}

func TestCrushExitMarker(t *testing.T) {
	cases := map[string]string{
		"out\nExit code 1":        "1",
		"out\nExit code 127\n":    "127",
		"Exit code 1\nmore":       "nil",
		"ok":                      "nil",
		"":                        "nil",
		"err\nexit code 3":        "nil",
		"Command was aborted ...": "nil",
	}
	for in, want := range cases {
		got := "nil"
		if c := crushExit(in); c != nil {
			got = fmt.Sprint(*c)
		}
		if got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestCrushWrongSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "crush.db")
	buildDB(t, p, `CREATE TABLE sessions (id TEXT PRIMARY KEY)`, `CREATE TABLE messages (id TEXT, session_id TEXT, content TEXT)`)
	_, err := Crush{}.Parse(Source{Agent: "crush", Path: p, Kind: "sqlite"}, "/x", time.Time{})
	if !errors.Is(err, ErrUnrecognized) {
		t.Fatalf("err = %v", err)
	}
}
