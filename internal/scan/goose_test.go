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

func gooseFixtureDB(t *testing.T, l layout, dbPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	n := 0
	msg := func(session, role string, sec int, content string) string {
		n++
		return fmt.Sprintf(`INSERT INTO messages (message_id, session_id, role, content_json, created_timestamp) VALUES ('m%d', %s, %s, %s, %d)`,
			n, ocSQL(session), ocSQL(role), ocSQL(l.expand(content)), ocT0.Unix()+int64(sec))
	}
	req := func(id, name, argsJSON string) string {
		return fmt.Sprintf(`[{"type":"toolRequest","id":%q,"toolCall":{"status":"success","value":{"name":%q,"arguments":%s}}}]`, id, name, argsJSON)
	}
	shellRes := func(id, stdout, exit string, isErr bool) string {
		sc := fmt.Sprintf(`{"stdout":%q,"stderr":""%s}`, stdout, exit)
		return fmt.Sprintf(`[{"type":"toolResponse","id":%q,"toolResult":{"status":"success","value":{"content":[{"type":"text","text":%q}],"structuredContent":%s,"isError":%v}}}]`, id, stdout, sc, isErr)
	}
	buildDB(t, dbPath,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', working_dir TEXT NOT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, message_id TEXT, session_id TEXT NOT NULL, role TEXT NOT NULL, content_json TEXT NOT NULL, created_timestamp INTEGER NOT NULL, metadata_json TEXT)`,
		fmt.Sprintf(`INSERT INTO sessions (id, working_dir) VALUES ('20261004_1', %s), ('20261004_2', %s)`, ocSQL(l.Root), ocSQL(l.expand("{{ELSEWHERE}}"))),
		msg("20261004_1", "assistant", 10, req("c1", "developer__shell", `{"command":"go test ./..."}`)),
		msg("20261004_1", "user", 12, shellRes("c1", "--- FAIL: TestAuth Authorization: Bearer SECRETTOKEN123", `,"exit_code":1`, true)),
		msg("20261004_1", "assistant", 60, req("c2", "developer__edit", `{"path":"{{ROOT}}/internal/auth/auth.go","before":"a","after":"b"}`)),
		msg("20261004_1", "user", 61, `[{"type":"toolResponse","id":"c2","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"ok"}],"isError":false}}}]`),
		msg("20261004_1", "assistant", 70, req("c2b", "developer__write", `{"path":"{{ROOT}}/internal/auth/failed.go","content":"x"}`)),
		msg("20261004_1", "user", 71, `[{"type":"toolResponse","id":"c2b","toolResult":{"status":"error","error":"permission denied"}}]`),
		msg("20261004_1", "assistant", 120, req("c3", "developer__shell", `{"command":"go test ./..."}`)),
		msg("20261004_1", "user", 125, shellRes("c3", "ok", `,"exit_code":0`, false)),
		msg("20261004_1", "assistant", 130, req("c4", "developer__shell", `{"command":"make test-unknown"}`)),
		msg("20261004_1", "user", 135, shellRes("c4", "", `,"timed_out":true`, true)),
		msg("20261004_1", "assistant", 140, req("c5", "developer__shell", `{"command":"ls -la"}`)),
		msg("20261004_1", "user", 141, shellRes("c5", "total 0", `,"exit_code":0`, false)),
		msg("20261004_2", "assistant", 150, req("c6", "developer__shell", `{"command":"make test-elsewhere"}`)),
		msg("20261004_2", "user", 151, shellRes("c6", "ok", `,"exit_code":0`, false)),
	)
}

func TestGoose(t *testing.T) {
	l := newLayout(t)
	gooseFixtureDB(t, l, filepath.Join(l.Home, ".local", "share", "goose", "sessions", "sessions.db"))
	st, events, _ := collect(t, Goose{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	for _, e := range events {
		if strings.Contains(e.Path, "failed.go") {
			t.Errorf("errored edit kept: %+v", e)
		}
	}
	checkContract(t, "goose", l, events, contract{paired: "go test ./...", after: "internal/auth/auth.go"})
}

func TestGooseLegacyJSONL(t *testing.T) {
	l := newLayout(t)
	dir := ".local/share/goose/sessions/"
	l.place(t, "goose/legacy.jsonl", dir+"20261004_140000.jsonl")
	l.place(t, "goose/legacy-elsewhere.jsonl", dir+"20261004_150000.jsonl")
	st, events, _ := collect(t, Goose{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	checkContract(t, "goose", l, events, contract{paired: "pytest -k auth", after: "app/auth.py"})

	// Once sessions.db exists goose has imported the jsonl files: skip them.
	gooseFixtureDB(t, l, filepath.Join(l.Home, dir, "sessions.db"))
	_, srcs := Goose{}.Discover(l.Home, env(nil))
	if len(srcs) != 1 || srcs[0].Kind != "sqlite" {
		t.Fatalf("got %v", srcs)
	}
}

func TestGoosePathRootOverride(t *testing.T) {
	l := newLayout(t)
	root := filepath.Join(l.Home, "gr")
	gooseFixtureDB(t, l, filepath.Join(root, "data", "sessions", "sessions.db"))
	_, srcs := Goose{}.Discover(l.Home, env(map[string]string{"GOOSE_PATH_ROOT": root}))
	if len(srcs) != 1 || srcs[0].Kind != "sqlite" {
		t.Fatalf("got %v", srcs)
	}
	if st, _ := (Goose{}).Discover(l.Home, env(nil)); st.State != "empty" {
		t.Fatalf("default root: %+v", st)
	}
}

func TestGooseWrongSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.db")
	buildDB(t, p, `CREATE TABLE sessions (id TEXT PRIMARY KEY, working_dir TEXT)`, `CREATE TABLE messages (id INTEGER, body TEXT)`)
	_, err := Goose{}.Parse(Source{Agent: "goose", Path: p, Kind: "sqlite"}, "/x", time.Time{})
	if !errors.Is(err, ErrUnrecognized) {
		t.Fatalf("err = %v", err)
	}
}
