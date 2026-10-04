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

// ocSQL quotes s as a SQL string literal (fixture helper for the SQLite
// adapter tests in opencode/goose/hermes/crush _test.go).
func ocSQL(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ocT0 is the fixture clock: 2026-10-04T14:00:00Z.
var ocT0 = time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC)

func ocMs(sec int) int64 { return ocT0.Add(time.Duration(sec) * time.Second).UnixMilli() }

func opencodeFixture(t *testing.T, l layout, dbPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	part := func(id, msg, session string, sec int, data string) string {
		return fmt.Sprintf(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (%s, %s, %s, %d, %d, %s)`,
			ocSQL(id), ocSQL(msg), ocSQL(session), ocMs(sec), ocMs(sec), ocSQL(l.expand(data)))
	}
	bash := func(cmd, status, output, exit string, sec int) string {
		return fmt.Sprintf(`{"type":"tool","callID":"call_%d","tool":"bash","state":{"status":%q,"input":{"command":%q,"description":"run"},"output":%q,"title":%q,"metadata":{"output":"x","exit":%s,"truncated":false},"time":{"start":%d,"end":%d}}}`,
			sec, status, cmd, output, cmd, exit, ocMs(sec), ocMs(sec+5))
	}
	buildDB(t, dbPath,
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		fmt.Sprintf(`INSERT INTO session VALUES ('ses_1', 'prj', 'a', %s, 't', '1.0', %d, %d)`, ocSQL(l.Root), ocMs(0), ocMs(0)),
		fmt.Sprintf(`INSERT INTO session VALUES ('ses_2', 'prj', 'b', %s, 't', '1.0', %d, %d)`, ocSQL(l.expand("{{ELSEWHERE}}")), ocMs(0), ocMs(0)),
		fmt.Sprintf(`INSERT INTO message VALUES ('msg_1', 'ses_1', %d, %d, %s)`, ocMs(0), ocMs(0), ocSQL(l.expand(`{"role":"assistant","path":{"cwd":"{{ROOT}}","root":"{{ROOT}}"}}`))),
		part("prt_1", "msg_1", "ses_1", 10, bash("npm test", "completed", "FAIL src/auth.test.ts\nAuthorization: Bearer SECRETTOKEN123", "1", 10)),
		part("prt_2", "msg_1", "ses_1", 60, `{"type":"tool","callID":"call_e","tool":"edit","state":{"status":"completed","input":{"filePath":"{{ROOT}}/src/auth.ts","oldString":"a","newString":"b"},"output":"ok","title":"src/auth.ts","metadata":{},"time":{"start":`+fmt.Sprint(ocMs(60))+`,"end":`+fmt.Sprint(ocMs(61))+`}}}`),
		part("prt_2b", "msg_1", "ses_1", 70, `{"type":"tool","callID":"call_f","tool":"write","state":{"status":"error","input":{"filePath":"{{ROOT}}/src/failed.ts"},"error":"denied","time":{"start":1,"end":2}}}`),
		part("prt_3", "msg_1", "ses_1", 120, bash("npm test", "completed", "ok 12 tests", "0", 120)),
		part("prt_4", "msg_1", "ses_1", 130, bash("make test-unknown", "completed", "killed", "null", 130)),
		part("prt_5", "msg_1", "ses_1", 140, bash("ls -la", "completed", "total 0", "0", 140)),
		part("prt_6", "msg_1", "ses_1", 150, `{"type":"text","text":"done"}`),
		part("prt_7", "msg_2", "ses_2", 160, bash("make test-elsewhere", "completed", "ok", "0", 160)),
	)
}

func TestOpenCode(t *testing.T) {
	l := newLayout(t)
	opencodeFixture(t, l, filepath.Join(l.Home, ".local", "share", "opencode", "opencode.db"))
	st, events, _ := collect(t, OpenCode{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	for _, e := range events {
		if strings.Contains(e.Path, "failed.ts") {
			t.Errorf("errored edit kept: %+v", e)
		}
	}
	checkContract(t, "opencode", l, events, contract{paired: "npm test", after: "src/auth.ts"})
}

func TestOpenCodeDataDirOverride(t *testing.T) {
	l := newLayout(t)
	dir := filepath.Join(l.Home, "oc")
	opencodeFixture(t, l, filepath.Join(dir, "opencode.db"))
	_, srcs := OpenCode{}.Discover(l.Home, env(map[string]string{"OPENCODE_DATA_DIR": dir}))
	if len(srcs) != 1 || srcs[0].Kind != "sqlite" {
		t.Fatalf("got %v", srcs)
	}
	_, srcs = OpenCode{}.Discover(l.Home, env(map[string]string{"OPENCODE_DATA_DIR": dir, "OPENCODE_DB": filepath.Join(dir, "opencode.db")}))
	if len(srcs) != 1 {
		t.Fatalf("OPENCODE_DB duplicate: %v", srcs)
	}
	st, srcs := OpenCode{}.Discover(l.Home, env(nil))
	if st.State != "empty" || len(srcs) != 0 {
		t.Fatalf("default root: %+v %v", st, srcs)
	}
}

func TestOpenCodeWrongSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "opencode.db")
	buildDB(t, p, `CREATE TABLE session (id TEXT PRIMARY KEY)`)
	_, err := OpenCode{}.Parse(Source{Agent: "opencode", Path: p, Kind: "sqlite"}, "/x", time.Time{})
	if !errors.Is(err, ErrUnrecognized) {
		t.Fatalf("err = %v", err)
	}
}
