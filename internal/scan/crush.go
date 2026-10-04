package scan

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Crush reads charmbracelet/crush's repo-local store <repo>/.crush/crush.db
// (config.go defaultDataDirectory ".crush"). It has no home-level log, so
// Discover reports empty and DiscoverRepo returns the database if present.
//
// Schema (internal/db/migrations): sessions(id, ...) with no cwd,
// messages(id, session_id, role, parts JSON, created_at seconds). parts is a
// list of {type, data} (internal/message/message.go partWrapper):
//
//	tool_call     {id, name, input (JSON string), finished}
//	tool_result   {tool_call_id, name, content, metadata (JSON string), is_error}
//	shell_command {command, output, exit_code}   bang-mode (!cmd) run by the user
//
// Exit status:
//   - shell_command: exit_code, a structured field always written.
//   - bash tool: BashResponseMetadata has no exit code. On a non-zero exit
//     the tool itself appends a final line "Exit code N" to the output
//     (agent/tools/bash.go formatOutput); that line, as the last line of
//     metadata.output, gives the failure. A zero exit leaves no trace, so a
//     bash call without the marker stays nil. Background jobs stay nil.
//
// Cwd: bash metadata.working_directory (or input.working_dir when absolute).
// Events with no recorded cwd (shell_command, edits) take the repo root,
// since crush.db lives in that repo and crush runs from it.
// Edits are edit/multiedit/write (input.file_path); is_error results drop.
type Crush struct{}

func (Crush) ID() string { return "crush" }

func (a Crush) Discover(home string, env func(string) string) (Status, []Source) {
	return Status{Agent: a.ID(), State: "empty", Detail: "repo-local only (.crush/crush.db)"}, nil
}

func (a Crush) DiscoverRepo(repoRoot string) []Source {
	p := filepath.Join(repoRoot, ".crush", "crush.db")
	if info, err := os.Stat(p); err != nil || info.IsDir() {
		return nil
	}
	return []Source{{Agent: a.ID(), Path: p, Kind: "sqlite"}}
}

var (
	crushTools = map[string]model.Kind{
		"bash":      model.KindShell,
		"edit":      model.KindEdit,
		"multiedit": model.KindEdit,
		"write":     model.KindEdit,
	}
	crushExitRe = regexp.MustCompile(`^Exit code (-?\d+)$`)
)

type crushRow struct {
	session, parts string
	ts             time.Time
}

func (a Crush) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	rows, err := crushRead(src.Path)
	if err != nil {
		return nil, err
	}
	// crush.db sits in <root>/.crush; events with no cwd ran there.
	defCwd := filepath.Dir(filepath.Dir(src.Path))
	b := newBook()
	for _, r := range rows {
		var parts []any
		if json.Unmarshal([]byte(r.parts), &parts) != nil {
			continue
		}
		base := model.Event{Agent: a.ID(), Session: r.session, TS: r.ts, Cwd: defCwd}
		for _, p := range parts {
			d := mapOf(p, "data")
			switch str(p, "type") {
			case "shell_command":
				cmd := strings.TrimSpace(str(d, "command"))
				if cmd == "" {
					continue
				}
				ev := base
				ev.Kind, ev.Command = model.KindShell, cmd
				ev.Exit = intOf(d, "exit_code")
				ev.StdoutHead = norm.Head(str(d, "output"))
				b.add("", ev)
			case "tool_call":
				in := args(d["input"])
				switch toolKind(str(d, "name"), crushTools) {
				case model.KindShell:
					cmd := shellCommand(in["command"])
					if cmd == "" {
						continue
					}
					ev := base
					ev.Kind, ev.Command = model.KindShell, cmd
					if wd := str(in, "working_dir"); filepath.IsAbs(wd) {
						ev.Cwd = wd
					}
					b.add(str(d, "id"), ev)
				case model.KindEdit:
					path := str(in, "file_path")
					if path == "" {
						continue
					}
					ev := base
					ev.Kind, ev.Path = model.KindEdit, path
					b.add(str(d, "id"), ev)
				}
			case "tool_result":
				e := b.get(str(d, "tool_call_id"))
				if e == nil {
					continue
				}
				isErr, _ := boolOf(d, "is_error")
				if e.ev.Kind == model.KindEdit {
					if isErr {
						e.drop = true
					}
					continue
				}
				meta := args(d["metadata"])
				if wd := str(meta, "working_directory"); filepath.IsAbs(wd) {
					e.ev.Cwd = wd
				}
				out := str(meta, "output")
				if out == "" {
					out = str(d, "content")
				}
				e.ev.StdoutHead = norm.Head(out)
				if bgJob, _ := boolOf(meta, "background"); bgJob || str(meta, "shell_id") != "" {
					continue
				}
				if code := crushExit(str(meta, "output")); code != nil && *code != 0 {
					e.ev.Exit = code
				}
			}
		}
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

// crushExit reads the "Exit code N" line bash.go appends as the last line.
func crushExit(out string) *int {
	lines := strings.Split(strings.TrimRight(out, "\n "), "\n")
	if len(lines) == 0 {
		return nil
	}
	return matchInt(crushExitRe, strings.TrimSpace(lines[len(lines)-1]))
}

func crushRead(path string) ([]crushRow, error) {
	db, err := openRO(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if !hasColumns(db, "sessions", "id") || !hasColumns(db, "messages", "session_id", "parts", "created_at") {
		return nil, unrecognized("no crush sessions/messages tables")
	}
	rows, err := db.Query(`SELECT session_id, parts, created_at FROM messages ORDER BY session_id, created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []crushRow
	for rows.Next() {
		var r crushRow
		var parts sql.NullString
		var created sql.NullInt64
		if err := rows.Scan(&r.session, &parts, &created); err != nil {
			return nil, err
		}
		r.parts = parts.String
		r.ts = unixAny(created.Int64)
		out = append(out, r)
	}
	return out, rows.Err()
}
