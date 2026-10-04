package scan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// CursorIDE reads the Cursor IDE's global state database, a fallback for
// agent chats the CLI transcripts do not cover:
//
//	Linux   ~/.config/Cursor/User/globalStorage/state.vscdb
//	macOS   ~/Library/Application Support/Cursor/User/globalStorage/state.vscdb
//	Windows %APPDATA%\Cursor\User\globalStorage\state.vscdb
//
// CURSOR_IDE_STATE_DB (comma-separated) replaces those paths. The database is
// opened read-only and never written.
//
// Table cursorDiskKV(key, value). Rows keyed bubbleId:<composerId>:<bubbleId>
// hold one chat bubble as JSON; an assistant bubble with a tool call carries
// toolFormerData {toolCallId, name, tool, params, rawArgs, result, status}
// where params/rawArgs/result are JSON strings or objects
// (kenn-io/agentsview#1798). composerData:<composerId> lists bubbles in
// conversation order in fullConversationHeadersOnly; without it bubbles are
// ordered by createdAt, then key.
//
// Shell tools are run_terminal_cmd / run_terminal_command_v2 (params.command,
// params.cwd when present). Exit status comes only from an explicit
// result.exitCode / result.exit_code. Cursor omits exitCode when it is zero
// (protobuf default), so a successful run usually carries no exit field and
// is skipped: a shell call without an explicit exit field emits nothing.
// status is never read as an exit status. Edit tools (edit_file,
// search_replace, write, ...) yield their target_file / relativeWorkspacePath;
// an edit whose status is "error" or "cancelled" or which carries an error is
// dropped. Bubbles record no cwd, so sessions usually match the repo through
// absolute edit paths (filterRepo). Field names beyond toolFormerData's
// envelope are partly UNVERIFIED and read strictly.
type CursorIDE struct{}

func (CursorIDE) ID() string { return "cursor-ide" }

func (c CursorIDE) Discover(home string, env func(string) string) (Status, []Source) {
	var paths []string
	if v := strings.TrimSpace(env("CURSOR_IDE_STATE_DB")); v != "" {
		paths = roots(env, "CURSOR_IDE_STATE_DB")
	} else {
		rel := filepath.Join("Cursor", "User", "globalStorage", "state.vscdb")
		cfg := env("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(home, ".config")
		}
		paths = []string{
			filepath.Join(cfg, rel),
			filepath.Join(home, "Library", "Application Support", rel),
		}
		if app := env("APPDATA"); app != "" {
			paths = append(paths, filepath.Join(app, rel))
		}
	}
	st := Status{Agent: c.ID()}
	var srcs []Source
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] || !exists(p) {
			continue
		}
		seen[p] = true
		srcs = append(srcs, Source{Agent: c.ID(), Path: p, Kind: "sqlite"})
	}
	if len(srcs) == 0 {
		st.State = "empty"
		st.Detail = "no state.vscdb at " + strings.Join(paths, ", ")
		return st, nil
	}
	st.State = "found"
	st.Detail = fmt.Sprintf("%d databases", len(srcs))
	return st, srcs
}

var cursorIDETools = map[string]model.Kind{
	"run_terminal_cmd":        model.KindShell,
	"run_terminal_command_v2": model.KindShell,
	"edit_file":               model.KindEdit,
	"edit_file_v2":            model.KindEdit,
	"search_replace":          model.KindEdit,
	"write":                   model.KindEdit,
	"multiedit":               model.KindEdit,
	"delete_file":             "",
}

type cursorIDEBubble struct {
	composer, bubble, key string
	value                 []byte
}

func (c CursorIDE) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	bubbles, order, err := cursorIDERead(src.Path)
	if err != nil {
		return nil, err
	}

	type item struct {
		ev  model.Event
		pos int
		key string
	}
	var items []item
	for _, bb := range bubbles {
		m := args(string(bb.value))
		tf := mapOf(m, "toolFormerData")
		if tf == nil {
			continue
		}
		ev := model.Event{Agent: c.ID(), Session: bb.composer, TS: parseTime(m["createdAt"])}
		p := args(tf["params"])
		if p == nil {
			p = args(tf["rawArgs"])
		}
		status := strings.ToLower(str(tf, "status"))
		switch toolKind(str(tf, "name"), cursorIDETools) {
		case model.KindShell:
			r := args(tf["result"])
			code := firstInt(r, "exitCode", "exit_code")
			if code == nil {
				continue
			}
			ev.Kind = model.KindShell
			ev.Command = shellCommand(p["command"])
			if ev.Command == "" {
				continue
			}
			ev.Exit = code
			if d := str(p, "cwd"); filepath.IsAbs(d) {
				ev.Cwd = d
			}
			ev.StdoutHead = norm.Head(str(r, "output"))
		case model.KindEdit:
			if status == "error" || status == "cancelled" || get(tf, "error") != nil {
				continue
			}
			ev.Kind = model.KindEdit
			for _, k := range []string{"target_file", "relativeWorkspacePath", "file_path", "path"} {
				if ev.Path = str(p, k); ev.Path != "" {
					break
				}
			}
			if ev.Path == "" {
				continue
			}
		default:
			continue
		}
		pos, ok := order[bb.composer][bb.bubble]
		if !ok {
			pos = 1 << 30
		}
		items = append(items, item{ev: ev, pos: pos, key: bb.key})
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.ev.Session != b.ev.Session {
			return a.ev.Session < b.ev.Session
		}
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		if !a.ev.TS.Equal(b.ev.TS) {
			return a.ev.TS.Before(b.ev.TS)
		}
		return a.key < b.key
	})
	var events []model.Event
	for _, it := range items {
		events = append(events, it.ev)
	}
	return filterRepo(events, repoRoot, since), nil
}

// cursorIDERead copies bubble rows and the conversation order out of the
// database and closes it.
func cursorIDERead(path string) ([]cursorIDEBubble, map[string]map[string]int, error) {
	db, err := openRO(path)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	if !hasColumns(db, "cursorDiskKV", "key", "value") {
		return nil, nil, unrecognized("no cursorDiskKV(key, value) table")
	}

	var bubbles []cursorIDEBubble
	rows, err := db.Query(`SELECT key, value FROM cursorDiskKV WHERE key LIKE 'bubbleId:%'`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var key string
		var val []byte
		if rows.Scan(&key, &val) != nil {
			continue
		}
		parts := strings.SplitN(key, ":", 3)
		if len(parts) != 3 || parts[1] == "" || len(val) == 0 {
			continue
		}
		bubbles = append(bubbles, cursorIDEBubble{composer: parts[1], bubble: parts[2], key: key, value: val})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	order := map[string]map[string]int{}
	rows, err = db.Query(`SELECT key, value FROM cursorDiskKV WHERE key LIKE 'composerData:%'`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var val []byte
		if rows.Scan(&key, &val) != nil {
			continue
		}
		id := strings.TrimPrefix(key, "composerData:")
		m := args(string(val))
		pos := map[string]int{}
		for i, h := range list(m, "fullConversationHeadersOnly") {
			if b := str(h, "bubbleId"); b != "" {
				pos[b] = i
			}
		}
		if len(pos) > 0 {
			order[id] = pos
		}
	}
	return bubbles, order, rows.Err()
}
