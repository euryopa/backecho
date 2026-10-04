package scan

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Cursor reads Cursor CLI (cursor-agent) transcripts:
// $CURSOR_HOME/projects/<project>/agent-transcripts/<uuid>/<uuid>.jsonl
// (default ~/.cursor), subagent transcripts in a subagents/ dir beside them.
//
// The format is closed-source and only partly documented by third-party
// parsers (engram#19, openclaw-graphiti-plugin#193, meq#2): each line is
// {role, message:{content:[...]}} with no top-level type, where content holds
// text, tool_use {id, name, input} and tool_result {tool_use_id, content,
// is_error} blocks; timestamps are weak or missing and no cwd is recorded.
// Field names below beyond that shape are UNVERIFIED, so the parser is strict:
// a line without role user/assistant and a message.content list is ignored,
// and a file with no such line is unrecognized.
//
// Exit status: none. No public source shows a structured exit field, so every
// shell event has Exit nil and is dropped downstream. is_error is never read
// as an exit status. Edits whose tool_result is_error is true are dropped.
// With no cwd, sessions match the repo through absolute edit paths only.
type Cursor struct{}

func (Cursor) ID() string { return "cursor" }

func (c Cursor) Discover(home string, env func(string) string) (Status, []Source) {
	var dirs []string
	for _, b := range roots(env, "CURSOR_HOME", filepath.Join(home, ".cursor")) {
		dirs = append(dirs, filepath.Join(b, "projects"))
	}
	return discoverFiles(c.ID(), dirs, func(p string, _ fs.DirEntry) bool {
		if !strings.HasSuffix(p, ".jsonl") {
			return false
		}
		for _, part := range strings.Split(filepath.ToSlash(p), "/") {
			if part == "agent-transcripts" {
				return true
			}
		}
		return false
	})
}

var cursorTools = map[string]model.Kind{
	"shell":            model.KindShell,
	"run_terminal_cmd": model.KindShell,
	"write":            model.KindEdit,
	"strreplace":       model.KindEdit,
	"edit_file":        model.KindEdit,
	"search_replace":   model.KindEdit,
	"multiedit":        model.KindEdit,
}

func (c Cursor) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	b := newBook()
	session := sessionFromPath(src.Path)
	recognized := false
	_, _, err := scanJSONL(src.Path, func(m obj) {
		role := str(m, "role")
		if role != "user" && role != "assistant" {
			return
		}
		content, ok := get(m, "message", "content").([]any)
		if !ok {
			return
		}
		recognized = true
		base := model.Event{Agent: c.ID(), Session: session, TS: parseTime(m["timestamp"])}
		for _, blk := range content {
			switch str(blk, "type") {
			case "tool_use":
				in := mapOf(blk, "input")
				switch toolKind(str(blk, "name"), cursorTools) {
				case model.KindShell:
					ev := base
					ev.Kind = model.KindShell
					ev.Command = shellCommand(in["command"])
					if ev.Command != "" {
						b.add(str(blk, "id"), ev)
					}
				case model.KindEdit:
					ev := base
					ev.Kind = model.KindEdit
					for _, k := range []string{"path", "file_path", "target_file"} {
						if ev.Path = str(in, k); ev.Path != "" {
							break
						}
					}
					if ev.Path != "" {
						b.add(str(blk, "id"), ev)
					}
				}
			case "tool_result":
				e := b.get(str(blk, "tool_use_id"))
				if e == nil {
					continue
				}
				isErr, _ := boolOf(blk, "is_error")
				if e.ev.Kind == model.KindEdit {
					if isErr {
						e.drop = true
					}
					continue
				}
				out := norm.Head(text(get(blk, "content")))
				if isErr {
					e.ev.StderrHead = out
				} else {
					e.ev.StdoutHead = out
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no Cursor agent-transcript role/message records")
	}
	return filterRepo(b.events(), repoRoot, since), nil
}
