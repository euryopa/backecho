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

// Goose reads block/goose sessions from <data>/sessions:
//
//   - GOOSE_PATH_ROOT set (comma-separated here): $GOOSE_PATH_ROOT/data/sessions
//   - else ${XDG_DATA_HOME:-~/.local/share}/goose/sessions,
//     ~/Library/Application Support/Block/goose/sessions (macOS) and
//     %APPDATA%/Block/goose/data/sessions (Windows)
//
// (crates/goose/src/config/paths.rs, etcetera app strategy "Block"/"goose").
//
// sessions.db (crates/goose/src/session/session_manager.rs, schema v16):
// sessions(id, working_dir, ...), messages(session_id, role, content_json,
// created_timestamp seconds). A directory without sessions.db is read as the
// legacy layout: <id>.jsonl whose first line is session metadata (working_dir)
// and every other line one Message {role, created, content}. Legacy files
// are left behind after goose imports them into sessions.db, so they are
// ignored when sessions.db exists.
//
// content is a list of MessageContent blocks (goose-provider-types
// conversation/message.rs): {type:"toolRequest", id, toolCall:{status,
// value:{name, arguments}}} and {type:"toolResponse", id, toolResult:{status,
// value: CallToolResult{content, structuredContent, isError} | [content]}}.
// Tool names are extension-prefixed: developer__shell, developer__write,
// developer__edit (path), legacy developer__text_editor (command, path).
//
// Exit comes only from the shell tool's structuredContent.exit_code
// (platform_extensions/developer/shell.rs ShellOutput; absent when the
// process was killed or timed out). Older goose shells wrote no exit code:
// those calls stay nil. Edits whose response is an error are dropped.
type Goose struct{}

func (Goose) ID() string { return "goose" }

func (g Goose) dirs(home string, env func(string) string) []string {
	if bases := roots(env, "GOOSE_PATH_ROOT"); len(bases) > 0 {
		var out []string
		for _, b := range bases {
			out = append(out, filepath.Join(b, "data", "sessions"))
		}
		return out
	}
	out := []string{
		filepath.Join(xdgData(home, env), "goose", "sessions"),
		filepath.Join(home, "Library", "Application Support", "Block", "goose", "sessions"),
	}
	if ad := env("APPDATA"); ad != "" {
		out = append(out, filepath.Join(ad, "Block", "goose", "data", "sessions"))
	}
	return out
}

func (g Goose) Discover(home string, env func(string) string) (Status, []Source) {
	dirs := g.dirs(home, env)
	st := Status{Agent: g.ID()}
	var srcs []Source
	for _, d := range dirs {
		db := filepath.Join(d, "sessions.db")
		if info, err := os.Stat(db); err == nil && !info.IsDir() {
			srcs = append(srcs, Source{Agent: g.ID(), Path: db, Kind: "sqlite"})
			continue
		}
		for _, f := range globFiles(filepath.Join(d, "*.jsonl")) {
			srcs = append(srcs, Source{Agent: g.ID(), Path: f, Kind: "file"})
		}
	}
	if len(srcs) == 0 {
		st.State = "empty"
		st.Detail = "no sessions under " + strings.Join(dirs, ", ")
		return st, nil
	}
	st.State = "found"
	st.Detail = fmt.Sprintf("%d sources", len(srcs))
	return st, srcs
}

var gooseTools = map[string]model.Kind{
	"shell":       model.KindShell,
	"write":       model.KindEdit,
	"edit":        model.KindEdit,
	"text_editor": model.KindEdit,
}

// gooseMsg is one message with the session it belongs to.
type gooseMsg struct {
	session, cwd string
	ts           time.Time
	content      []any
}

func (g Goose) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	var msgs []gooseMsg
	var err error
	if src.Kind == "sqlite" {
		msgs, err = gooseReadDB(src.Path)
	} else {
		msgs, err = gooseReadJSONL(src.Path)
	}
	if err != nil {
		return nil, err
	}
	b := newBook()
	for _, m := range msgs {
		for _, blk := range m.content {
			switch str(blk, "type") {
			case "toolRequest":
				if str(blk, "toolCall", "status") != "success" {
					continue
				}
				call := mapOf(blk, "toolCall", "value")
				name := str(call, "name")
				kind := toolKind(name, gooseTools)
				a := args(call["arguments"])
				base := model.Event{Agent: g.ID(), Session: m.session, TS: m.ts, Cwd: m.cwd}
				switch kind {
				case model.KindShell:
					cmd := shellCommand(a["command"])
					if cmd == "" {
						continue
					}
					ev := base
					ev.Kind, ev.Command = model.KindShell, cmd
					b.add(str(blk, "id"), ev)
				case model.KindEdit:
					if strings.HasSuffix(strings.ToLower(name), "text_editor") {
						switch str(a, "command") {
						case "write", "str_replace", "insert", "edit_file":
						default:
							continue
						}
					}
					path := str(a, "path")
					if path == "" {
						continue
					}
					ev := base
					ev.Kind, ev.Path = model.KindEdit, path
					b.add(str(blk, "id"), ev)
				}
			case "toolResponse":
				e := b.get(str(blk, "id"))
				if e == nil {
					continue
				}
				res := mapOf(blk, "toolResult")
				isErr, _ := boolOf(res, "value", "isError")
				failed := str(res, "status") != "success" || isErr
				if e.ev.Kind == model.KindEdit {
					if failed {
						e.drop = true
					}
					continue
				}
				sc := mapOf(res, "value", "structuredContent")
				if code := intOf(sc, "exit_code"); code != nil {
					e.ev.Exit = code
				}
				if s := norm.Head(str(sc, "stdout")); s != "" {
					e.ev.StdoutHead = s
				} else if sc == nil {
					v := get(res, "value", "content")
					if v == nil {
						v = get(res, "value")
					}
					e.ev.StdoutHead = norm.Head(text(v))
				}
				e.ev.StderrHead = norm.Head(str(sc, "stderr"))
			}
		}
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

func gooseReadDB(path string) ([]gooseMsg, error) {
	db, err := openRO(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if !hasColumns(db, "sessions", "id", "working_dir") || !hasColumns(db, "messages", "session_id", "content_json", "created_timestamp") {
		return nil, unrecognized("no goose sessions/messages tables")
	}
	cwds := map[string]string{}
	rows, err := db.Query(`SELECT id, COALESCE(working_dir, '') FROM sessions`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, wd string
		if err := rows.Scan(&id, &wd); err != nil {
			rows.Close()
			return nil, err
		}
		cwds[id] = wd
	}
	rows.Close()

	rows, err = db.Query(`SELECT session_id, content_json, created_timestamp FROM messages ORDER BY session_id, created_timestamp, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gooseMsg
	for rows.Next() {
		var session string
		var content sql.NullString
		var created sql.NullInt64
		if err := rows.Scan(&session, &content, &created); err != nil {
			return nil, err
		}
		var blocks []any
		if json.Unmarshal([]byte(content.String), &blocks) != nil {
			continue
		}
		out = append(out, gooseMsg{session: session, cwd: cwds[session], ts: unixAny(created.Int64), content: blocks})
	}
	return out, rows.Err()
}

func gooseReadJSONL(path string) ([]gooseMsg, error) {
	session := sessionFromPath(path)
	var cwd string
	first, recognized := true, false
	var out []gooseMsg
	_, _, err := scanJSONL(path, func(m obj) {
		if first {
			first = false
			if _, ok := m["working_dir"]; ok && m["content"] == nil {
				recognized = true
				cwd = str(m, "working_dir")
				return
			}
		}
		if !recognized || str(m, "role") == "" {
			return
		}
		out = append(out, gooseMsg{session: session, cwd: cwd, ts: parseTime(m["created"]), content: list(m, "content")})
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no goose session metadata line")
	}
	return out, nil
}
