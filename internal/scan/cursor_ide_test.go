package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// cursorIDERow renders one cursorDiskKV insert. params and result are stored
// as JSON strings inside the bubble, as Cursor does.
func cursorIDERow(t *testing.T, composer, bubble, createdAt string, tf map[string]any) string {
	t.Helper()
	for _, k := range []string{"params", "result"} {
		if v, ok := tf[k]; ok {
			b, _ := json.Marshal(v)
			tf[k] = string(b)
		}
	}
	v, err := json.Marshal(map[string]any{"type": 2, "bubbleId": bubble, "createdAt": createdAt, "toolFormerData": tf})
	if err != nil {
		t.Fatal(err)
	}
	return "INSERT INTO cursorDiskKV VALUES ('bubbleId:" + composer + ":" + bubble + "', '" + strings.ReplaceAll(string(v), "'", "''") + "')"
}

func cursorIDEFixture(t *testing.T, l layout, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(l.Root), "other")
	type tf = map[string]any
	// Bubble ids are deliberately out of conversation order; composerData
	// gives the real order.
	stmts := []string{
		`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
		cursorIDERow(t, "c1", "b9", "2026-10-04T10:01:00Z", tf{"toolCallId": "t1", "name": "run_terminal_cmd", "status": "completed",
			"params": tf{"command": "npm test", "is_background": false},
			"result": tf{"output": "FAIL auth: Authorization: Bearer SECRETTOKEN123", "exitCode": 1}}),
		cursorIDERow(t, "c1", "b2", "2026-10-04T10:03:00Z", tf{"toolCallId": "t2", "name": "edit_file", "status": "completed",
			"params": tf{"target_file": l.Root + "/src/auth.ts", "instructions": "fix"}}),
		cursorIDERow(t, "c1", "b5", "2026-10-04T10:03:30Z", tf{"toolCallId": "t3", "name": "edit_file", "status": "error",
			"params": tf{"target_file": l.Root + "/src/refused.ts"}}),
		cursorIDERow(t, "c1", "b1", "2026-10-04T10:05:00Z", tf{"toolCallId": "t4", "name": "run_terminal_cmd", "status": "completed",
			"params": tf{"command": "npm test"}, "result": tf{"output": "PASS auth", "exitCode": 0}}),
		cursorIDERow(t, "c1", "b7", "2026-10-04T10:06:00Z", tf{"toolCallId": "t5", "name": "run_terminal_cmd", "status": "completed",
			"params": tf{"command": "make test-unknown"}, "result": tf{"output": "done"}}),
		cursorIDERow(t, "c1", "b3", "2026-10-04T10:07:00Z", tf{"toolCallId": "t6", "name": "run_terminal_cmd", "status": "completed",
			"params": tf{"command": "ls -la"}, "result": tf{"output": "total 0", "exitCode": 0}}),
		cursorIDERow(t, "c2", "x1", "2026-10-04T11:00:00Z", tf{"toolCallId": "t7", "name": "run_terminal_cmd", "status": "completed",
			"params": tf{"command": "make test-elsewhere", "cwd": other}, "result": tf{"output": "ok", "exitCode": 0}}),
		cursorIDERow(t, "c2", "x2", "2026-10-04T11:01:00Z", tf{"toolCallId": "t8", "name": "edit_file", "status": "completed",
			"params": tf{"target_file": other + "/main.go"}}),
		`INSERT INTO cursorDiskKV VALUES ('composerData:c1', '{"composerId":"c1","fullConversationHeadersOnly":[{"bubbleId":"b9","type":2},{"bubbleId":"b2","type":2},{"bubbleId":"b5","type":2},{"bubbleId":"b1","type":2},{"bubbleId":"b7","type":2},{"bubbleId":"b3","type":2}]}')`,
		`INSERT INTO cursorDiskKV VALUES ('bubbleId:c1:b0', '{"type":1,"text":"fix the auth tests"}')`,
	}
	buildDB(t, path, stmts...)
}

func TestCursorIDE(t *testing.T) {
	l := newLayout(t)
	p := filepath.Join(l.Home, ".config", "Cursor", "User", "globalStorage", "state.vscdb")
	cursorIDEFixture(t, l, p)
	before, _ := os.ReadFile(p)

	st, events, _ := collect(t, CursorIDE{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	checkContract(t, "cursor-ide", l, events, contract{paired: "npm test", after: "src/auth.ts"})

	for _, e := range events {
		if e.Kind == model.KindShell && (e.Exit == nil || e.Command == unknownExit) {
			t.Errorf("shell event without explicit exit emitted: %+v", e)
		}
		if strings.HasSuffix(e.Path, "refused.ts") {
			t.Errorf("errored edit kept: %+v", e)
		}
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Error("database modified")
	}
}

func TestCursorIDEOverrideAndUnrecognized(t *testing.T) {
	l := newLayout(t)
	p := filepath.Join(l.Home, "custom.vscdb")
	buildDB(t, p, `CREATE TABLE ItemTable (key TEXT, value BLOB)`)
	st, srcs := CursorIDE{}.Discover(l.Home, env(map[string]string{"CURSOR_IDE_STATE_DB": p}))
	if st.State != "found" || len(srcs) != 1 || srcs[0].Kind != "sqlite" {
		t.Fatalf("%+v %v", st, srcs)
	}
	_, err := CursorIDE{}.Parse(srcs[0], l.Root, time.Time{})
	if err == nil || !strings.Contains(err.Error(), ErrUnrecognized.Error()) {
		t.Fatalf("err = %v", err)
	}

	win := filepath.Join(l.Home, "AppData", "Roaming")
	cursorIDEFixture(t, l, filepath.Join(win, "Cursor", "User", "globalStorage", "state.vscdb"))
	if st, srcs := (CursorIDE{}).Discover(l.Home, env(map[string]string{"APPDATA": win})); st.State != "found" || len(srcs) != 1 {
		t.Fatalf("APPDATA: %+v %v", st, srcs)
	}
}
