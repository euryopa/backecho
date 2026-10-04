package scan

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// OpenCode reads sst/opencode's SQLite store:
// ${XDG_DATA_HOME:-~/.local/share}/opencode/opencode.db (xdg-basedir is used on
// every platform). Development channels write opencode-<channel>.db next to
// it; those are read too. OPENCODE_DATA_DIR (comma-separated) replaces the
// data directory; OPENCODE_DB, opencode's own override, names the file
// (absolute, or relative to the data directory).
//
// Verified against sst/opencode packages/core/src/session/sql.ts and
// packages/schema/src/v1/session.ts (Oct 2026):
//
//	session(id, directory, ...)            directory = instance cwd, absolute
//	message(id, session_id, data JSON)     assistant data.path.cwd
//	part(id, message_id, session_id, time_created, data JSON)
//
// A tool part is {type:"tool", callID, tool, state:{status, input, output,
// metadata, time:{start,end}}}. The shell tool keeps the id "bash"
// (packages/opencode/src/tool/shell.ts) and returns metadata.exit = the
// process exit code, or null when the process was killed/timed out.
// Exit comes only from state.metadata.exit; anything else stays nil.
// Cwd is input.workdir (joined to the session directory when relative), else
// the session directory, else the message's path.cwd.
// Edits are edit/write/multiedit (input.filePath) and apply_patch
// (input.patchText); only parts with state.status "completed" are kept.
// The older JSON-file layout under storage/ is not read.
type OpenCode struct{}

func (OpenCode) ID() string { return "opencode" }

func (a OpenCode) Discover(home string, env func(string) string) (Status, []Source) {
	dirs := roots(env, "OPENCODE_DATA_DIR", filepath.Join(xdgData(home, env), "opencode"))
	var files []string
	if db := strings.TrimSpace(env("OPENCODE_DB")); db != "" && db != ":memory:" {
		if filepath.IsAbs(db) {
			files = append(files, db)
		} else {
			for _, d := range dirs {
				files = append(files, filepath.Join(d, db))
			}
		}
	}
	for _, d := range dirs {
		files = append(files, filepath.Join(d, "opencode.db"))
		files = append(files, globFiles(filepath.Join(d, "opencode-*.db"))...)
	}
	return opencodeDiscoverDB(a.ID(), dirs, files)
}

// opencodeDiscoverDB is the Discover shared by the SQLite adapters in
// opencode.go, goose.go and hermes.go: candidates that exist become sources.
func opencodeDiscoverDB(agent string, dirs, candidates []string) (Status, []Source) {
	st := Status{Agent: agent}
	var srcs []Source
	seen := map[string]bool{}
	for _, f := range candidates {
		if seen[f] {
			continue
		}
		seen[f] = true
		if info, err := os.Stat(f); err == nil && !info.IsDir() {
			srcs = append(srcs, Source{Agent: agent, Path: f, Kind: "sqlite"})
		}
	}
	if len(srcs) == 0 {
		st.State = "empty"
		st.Detail = "no database under " + strings.Join(dirs, ", ")
		return st, nil
	}
	st.State = "found"
	st.Detail = fmt.Sprintf("%d databases", len(srcs))
	return st, srcs
}

var opencodeTools = map[string]model.Kind{
	"bash":        model.KindShell,
	"edit":        model.KindEdit,
	"write":       model.KindEdit,
	"multiedit":   model.KindEdit,
	"apply_patch": model.KindEdit,
	"patch":       model.KindEdit,
}

type opencodePart struct {
	session string
	created int64
	data    string
	cwd     string // message data.path.cwd
}

func (a OpenCode) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	db, err := openRO(src.Path)
	if err != nil {
		return nil, err
	}
	dirs, parts, err := opencodeRead(db)
	db.Close()
	if err != nil {
		return nil, err
	}

	var out []model.Event
	for _, p := range parts {
		var d obj
		if json.Unmarshal([]byte(p.data), &d) != nil || str(d, "type") != "tool" {
			continue
		}
		kind := toolKind(str(d, "tool"), opencodeTools)
		if kind == "" {
			continue
		}
		state := mapOf(d, "state")
		input := mapOf(state, "input")
		ts := parseTime(get(state, "time", "start"))
		if ts.IsZero() {
			ts = unixAny(p.created)
		}
		cwd := dirs[p.session]
		if cwd == "" {
			cwd = p.cwd
		}
		base := model.Event{Agent: a.ID(), Session: p.session, TS: ts, Cwd: cwd}
		switch kind {
		case model.KindShell:
			ev := base
			ev.Kind = model.KindShell
			ev.Command = shellCommand(input["command"])
			if ev.Command == "" {
				continue
			}
			if wd := str(input, "workdir"); wd != "" {
				if filepath.IsAbs(wd) {
					ev.Cwd = wd
				} else if cwd != "" {
					ev.Cwd = filepath.Join(cwd, wd)
				}
			}
			ev.Exit = intOf(state, "metadata", "exit")
			ev.StdoutHead = norm.Head(str(state, "output"))
			out = append(out, ev)
		case model.KindEdit:
			if str(state, "status") != "completed" {
				continue
			}
			var paths []string
			if fp := str(input, "filePath"); fp != "" {
				paths = append(paths, fp)
			} else if pt := str(input, "patchText"); pt != "" {
				paths = patchPaths(pt)
			}
			for _, path := range paths {
				ev := base
				ev.Kind, ev.Path = model.KindEdit, path
				out = append(out, ev)
			}
		}
	}
	return filterRepo(out, repoRoot, since), nil
}

func opencodeRead(db *sql.DB) (map[string]string, []opencodePart, error) {
	if !hasColumns(db, "session", "id", "directory") || !hasColumns(db, "part", "session_id", "message_id", "data") {
		return nil, nil, unrecognized("no opencode session/part tables")
	}
	dirs := map[string]string{}
	rows, err := db.Query(`SELECT id, COALESCE(directory, '') FROM session`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, dir string
		if err := rows.Scan(&id, &dir); err != nil {
			rows.Close()
			return nil, nil, err
		}
		dirs[id] = dir
	}
	rows.Close()

	msgCwd := map[string]string{}
	if hasColumns(db, "message", "id", "data") {
		rows, err := db.Query(`SELECT id, data FROM message`)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id string
			var data sql.NullString
			if rows.Scan(&id, &data) != nil {
				continue
			}
			if !strings.Contains(data.String, `"cwd"`) {
				continue
			}
			var m obj
			if json.Unmarshal([]byte(data.String), &m) == nil {
				if c := str(m, "path", "cwd"); c != "" {
					msgCwd[id] = c
				}
			}
		}
		rows.Close()
	}

	order := "rowid"
	created := "0"
	if hasColumns(db, "part", "time_created") {
		order = "time_created, id"
		created = "COALESCE(time_created, 0)"
	}
	rows, err = db.Query(`SELECT session_id, message_id, ` + created + `, data FROM part ORDER BY ` + order)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var parts []opencodePart
	for rows.Next() {
		var p opencodePart
		var msg string
		var data sql.NullString
		if err := rows.Scan(&p.session, &msg, &p.created, &data); err != nil {
			return nil, nil, err
		}
		p.data = data.String
		p.cwd = msgCwd[msg]
		parts = append(parts, p)
	}
	return dirs, parts, rows.Err()
}
