package scan

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Hermes reads NousResearch/hermes-agent's SQLite state: <home>/state.db and
// <home>/profiles/<name>/state.db, where <home> is HERMES_HOME
// (comma-separated here) or ~/.hermes (%LOCALAPPDATA%/hermes on Windows;
// hermes_constants.py).
//
// Schema (hermes_state_common.py SCHEMA_SQL, Oct 2026):
//
//	sessions(id, cwd, git_branch, ...)    cwd/git_branch are optional (older dbs)
//	messages(session_id, role, content, tool_call_id, tool_calls, timestamp REAL)
//
// Assistant rows carry tool_calls as OpenAI JSON [{id, function:{name,
// arguments}}]; the matching role="tool" row has tool_call_id and the tool's
// JSON result as content. The shell tool is "terminal" {command, workdir,
// background}; its result is {"output", "exit_code", "error"}
// (tools/terminal_tool.py, terminal_tool_result.py).
//
// Exit is the result's exit_code, only when error is empty, exit_code is a
// non-negative integer and the call was not a background spawn (background
// spawns report exit_code 0 with a pid; tool-level failures report -1 or a
// guard's 1 together with an error, for a command that never ran). Cwd is
// workdir when absolute, else the session cwd.
// Edits are write_file and patch (path, or V4A patch text); a result with a
// non-empty "error" or success:false is dropped.
type Hermes struct{}

func (Hermes) ID() string { return "hermes" }

func (h Hermes) Discover(home string, env func(string) string) (Status, []Source) {
	def := []string{filepath.Join(home, ".hermes")}
	if la := env("LOCALAPPDATA"); la != "" {
		def = append(def, filepath.Join(la, "hermes"))
	}
	dirs := roots(env, "HERMES_HOME", def...)
	var files []string
	for _, d := range dirs {
		files = append(files, filepath.Join(d, "state.db"))
		files = append(files, globFiles(filepath.Join(d, "profiles", "*", "state.db"))...)
	}
	return opencodeDiscoverDB(h.ID(), dirs, files)
}

var hermesTools = map[string]model.Kind{
	"terminal":   model.KindShell,
	"write_file": model.KindEdit,
	"patch":      model.KindEdit,
}

type hermesSession struct{ cwd, branch string }

type hermesRow struct {
	session, role, content, callID, calls string
	ts                                    time.Time
}

func (h Hermes) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	sessions, rows, err := hermesRead(src.Path)
	if err != nil {
		return nil, err
	}
	b := newBook()
	bg := map[*entry]bool{}
	edits := map[string][]*entry{} // one patch call can touch several files
	for _, r := range rows {
		s := sessions[r.session]
		switch r.role {
		case "assistant":
			if r.calls == "" {
				continue
			}
			var calls []any
			if json.Unmarshal([]byte(r.calls), &calls) != nil {
				continue
			}
			for _, c := range calls {
				name := str(c, "function", "name")
				a := args(get(c, "function", "arguments"))
				id := str(c, "id")
				if id == "" {
					id = str(c, "call_id")
				}
				base := model.Event{Agent: h.ID(), Session: r.session, TS: r.ts, Cwd: s.cwd, Branch: s.branch}
				switch toolKind(name, hermesTools) {
				case model.KindShell:
					cmd := shellCommand(a["command"])
					if cmd == "" {
						continue
					}
					ev := base
					ev.Kind, ev.Command = model.KindShell, cmd
					if wd := str(a, "workdir"); filepath.IsAbs(wd) {
						ev.Cwd = wd
					}
					e := b.add(id, ev)
					if v, _ := boolOf(a, "background"); v {
						bg[e] = true
					}
				case model.KindEdit:
					var paths []string
					if p := str(a, "path"); p != "" {
						paths = append(paths, p)
					} else if p := str(a, "patch"); p != "" {
						paths = patchPaths(p)
					}
					for _, p := range paths {
						ev := base
						ev.Kind, ev.Path = model.KindEdit, p
						edits[id] = append(edits[id], b.add("", ev))
					}
				}
			}
		case "tool":
			var res obj
			if json.Unmarshal([]byte(strings.TrimSpace(r.content)), &res) != nil {
				res = nil
			}
			errText := str(res, "error")
			if es := edits[r.callID]; r.callID != "" && len(es) > 0 {
				ok, has := boolOf(res, "success")
				if errText != "" || (has && !ok) {
					for _, x := range es {
						x.drop = true
					}
				}
				continue
			}
			e := b.get(r.callID)
			if e == nil {
				continue
			}
			if res == nil {
				e.ev.StdoutHead = norm.Head(r.content)
				continue
			}
			e.ev.StdoutHead = norm.Head(str(res, "output"))
			if errText != "" || bg[e] || res["pid"] != nil || str(res, "session_id") != "" {
				continue
			}
			if code := intOf(res, "exit_code"); code != nil && *code >= 0 {
				e.ev.Exit = code
			}
		}
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

func hermesRead(path string) (map[string]hermesSession, []hermesRow, error) {
	db, err := openRO(path)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	if !hasColumns(db, "sessions", "id") || !hasColumns(db, "messages", "session_id", "role", "content", "tool_call_id", "tool_calls", "timestamp") {
		return nil, nil, unrecognized("no hermes sessions/messages tables")
	}
	cols := columns(db, "sessions")
	cwd, branch := "''", "''"
	if cols["cwd"] {
		cwd = "COALESCE(cwd, '')"
	}
	if cols["git_branch"] {
		branch = "COALESCE(git_branch, '')"
	}
	sessions := map[string]hermesSession{}
	rows, err := db.Query(`SELECT id, ` + cwd + `, ` + branch + ` FROM sessions`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		var s hermesSession
		if err := rows.Scan(&id, &s.cwd, &s.branch); err != nil {
			rows.Close()
			return nil, nil, err
		}
		sessions[id] = s
	}
	rows.Close()

	rows, err = db.Query(`SELECT session_id, role, content, tool_call_id, tool_calls, timestamp FROM messages ORDER BY session_id, timestamp, id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []hermesRow
	for rows.Next() {
		var r hermesRow
		var content, callID, calls sql.NullString
		var ts sql.NullFloat64
		if err := rows.Scan(&r.session, &r.role, &content, &callID, &calls, &ts); err != nil {
			return nil, nil, err
		}
		r.content, r.callID, r.calls = content.String, callID.String, calls.String
		r.ts = hermesTime(ts.Float64)
		out = append(out, r)
	}
	return sessions, out, rows.Err()
}

func hermesTime(f float64) time.Time {
	if f <= 0 {
		return time.Time{}
	}
	if f > 1e11 { // milliseconds
		return time.UnixMilli(int64(f)).UTC()
	}
	return time.Unix(0, int64(f*1e9)).UTC()
}
