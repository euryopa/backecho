package scan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hermesFixture(t *testing.T, l layout, dbPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := func(sec int) string { return fmt.Sprintf("%.3f", float64(ocT0.Unix()+int64(sec))+0.25) }
	call := func(session string, sec int, id, name, argsJSON string) string {
		calls := fmt.Sprintf(`[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]`, id, name, l.expand(argsJSON))
		return fmt.Sprintf(`INSERT INTO messages (session_id, role, content, tool_calls, timestamp) VALUES (%s, 'assistant', '', %s, %s)`, ocSQL(session), ocSQL(calls), ts(sec))
	}
	result := func(session string, sec int, id, name, content string) string {
		return fmt.Sprintf(`INSERT INTO messages (session_id, role, content, tool_call_id, tool_name, timestamp) VALUES (%s, 'tool', %s, %s, %s, %s)`, ocSQL(session), ocSQL(content), ocSQL(id), ocSQL(name), ts(sec))
	}
	buildDB(t, dbPath,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, started_at REAL NOT NULL, cwd TEXT, git_branch TEXT)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL, content TEXT, tool_call_id TEXT, tool_calls TEXT, tool_name TEXT, timestamp REAL NOT NULL, token_count INTEGER)`,
		fmt.Sprintf(`INSERT INTO sessions VALUES ('s1', 'cli', %s, %s, 'main'), ('s2', 'cli', %s, %s, NULL)`, ts(0), ocSQL(l.Root), ts(0), ocSQL(l.expand("{{ELSEWHERE}}"))),
		`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('s1', 'user', 'fix the auth tests', `+ts(1)+`)`,
		call("s1", 10, "call_1", "terminal", `{"command":"npm test","timeout":120}`),
		result("s1", 12, "call_1", "terminal", `{"output": "FAIL auth.test.ts\nAuthorization: Bearer SECRETTOKEN123", "exit_code": 1, "error": null}`),
		call("s1", 60, "call_2", "patch", `{"path":"{{ROOT}}/src/auth.ts","old_string":"a","new_string":"b"}`),
		result("s1", 61, "call_2", "patch", `{"success": true, "diff": "..."}`),
		call("s1", 70, "call_2b", "write_file", `{"path":"{{ROOT}}/src/failed.ts","content":"x"}`),
		result("s1", 71, "call_2b", "write_file", `{"error": "Permission denied"}`),
		call("s1", 120, "call_3", "terminal", `{"command":"npm test","workdir":"{{ROOT}}"}`),
		result("s1", 125, "call_3", "terminal", `{"output": "12 passing", "exit_code": 0, "error": null}`),
		call("s1", 130, "call_4", "terminal", `{"command":"make test-unknown","background":true}`),
		result("s1", 131, "call_4", "terminal", `{"output": "Background process started", "session_id": "proc_1", "pid": 4242, "exit_code": 0, "error": null}`),
		call("s1", 135, "call_4b", "terminal", `{"command":"npm test -- --blocked"}`),
		result("s1", 136, "call_4b", "terminal", `{"output": "", "exit_code": -1, "error": "Command blocked"}`),
		call("s1", 140, "call_5", "terminal", `{"command":"ls -la"}`),
		result("s1", 141, "call_5", "terminal", `{"output": "total 0", "exit_code": 0, "error": null}`),
		call("s2", 150, "call_6", "terminal", `{"command":"make test-elsewhere"}`),
		result("s2", 151, "call_6", "terminal", `{"output": "ok", "exit_code": 0, "error": null}`),
	)
}

func TestHermes(t *testing.T) {
	l := newLayout(t)
	hermesFixture(t, l, filepath.Join(l.Home, ".hermes", "state.db"))
	st, events, _ := collect(t, Hermes{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	for _, e := range events {
		if strings.Contains(e.Path, "failed.ts") {
			t.Errorf("errored edit kept: %+v", e)
		}
		if (strings.Contains(e.Command, "test-unknown") || strings.Contains(e.Command, "blocked")) && e.Exit != nil {
			t.Errorf("exit inferred: %+v", e)
		}
	}
	checkContract(t, "hermes", l, events, contract{paired: "npm test", after: "src/auth.ts"})
}

func TestHermesHomeOverrideAndProfiles(t *testing.T) {
	l := newLayout(t)
	hh := filepath.Join(l.Home, "hh")
	hermesFixture(t, l, filepath.Join(hh, "state.db"))
	hermesFixture(t, l, filepath.Join(hh, "profiles", "work", "state.db"))
	_, srcs := Hermes{}.Discover(l.Home, env(map[string]string{"HERMES_HOME": hh}))
	if len(srcs) != 2 || srcs[0].Kind != "sqlite" {
		t.Fatalf("got %v", srcs)
	}
	if st, _ := (Hermes{}).Discover(l.Home, env(nil)); st.State != "empty" {
		t.Fatalf("default root: %+v", st)
	}
}

func TestHermesWrongSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	buildDB(t, p, `CREATE TABLE sessions (id TEXT PRIMARY KEY)`, `CREATE TABLE messages (id INTEGER, session_id TEXT, role TEXT, content TEXT)`)
	_, err := Hermes{}.Parse(Source{Agent: "hermes", Path: p, Kind: "sqlite"}, "/x", time.Time{})
	if !errors.Is(err, ErrUnrecognized) {
		t.Fatalf("err = %v", err)
	}
}
