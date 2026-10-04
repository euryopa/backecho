package scan

import (
	"path/filepath"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Droid reads Factory Droid sessions: $DROID_SESSIONS_DIR, else
// ~/.factory/sessions/<project-slug>/<uuid>.jsonl (the <uuid>.settings.json
// beside each file is skipped).
//
// The first line is {"type":"session_start", id, title, cwd}; every other line
// is {"type":"message", id, timestamp, message:{role, content:[...]}} with
// Anthropic-style tool_use / tool_result blocks. Execute is the shell tool;
// Edit, Create, MultiEdit and ApplyPatch are edits (edits whose tool_result is
// is_error are dropped).
//
// Exit status comes only from a structured exitCode / exit_code field on the
// tool_result block (Factory's Sessions API documents exitCode on tool_result).
// Text in the result is never read for an exit code, so a result without the
// field stays unknown and is dropped downstream.
//
// Unverified: Droid is closed source. The line envelope is taken from
// sageox/ox cmd/ox-adapter-droid (checked against droid 0.126.0); tool names
// from Factory's hooks reference; the on-disk exitCode field is not confirmed.
type Droid struct{}

func (Droid) ID() string { return "droid" }

func (d Droid) Discover(home string, env func(string) string) (Status, []Source) {
	dirs := roots(env, "DROID_SESSIONS_DIR", filepath.Join(home, ".factory", "sessions"))
	return discoverFiles(d.ID(), dirs, hasExt(".jsonl"))
}

var droidTools = map[string]model.Kind{
	"execute":    model.KindShell,
	"edit":       model.KindEdit,
	"create":     model.KindEdit,
	"multiedit":  model.KindEdit,
	"applypatch": model.KindEdit,
}

func (d Droid) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	b := newBook()
	session := sessionFromPath(src.Path)
	var cwd string
	edits := map[string][]*entry{}
	recognized := false
	_, _, err := scanJSONL(src.Path, func(m obj) {
		switch str(m, "type") {
		case "session_start":
			recognized = true
			if id := str(m, "id"); id != "" {
				session = id
			}
			cwd = str(m, "cwd")
		case "message":
			msg := mapOf(m, "message")
			if msg == nil {
				return
			}
			recognized = true
			base := model.Event{Agent: d.ID(), Session: session, TS: parseTime(m["timestamp"]), Cwd: cwd}
			for _, blk := range list(msg, "content") {
				switch str(blk, "type") {
				case "tool_use":
					in := mapOf(blk, "input")
					switch toolKind(str(blk, "name"), droidTools) {
					case model.KindShell:
						ev := base
						ev.Kind = model.KindShell
						ev.Command = shellCommand(in["command"])
						if ev.Command == "" {
							continue
						}
						if wd := str(in, "workdir"); wd != "" {
							ev.Cwd = wd
						} else if wd := str(in, "cwd"); wd != "" {
							ev.Cwd = wd
						}
						b.add(str(blk, "id"), ev)
					case model.KindEdit:
						var paths []string
						if p := str(in, "file_path"); p != "" {
							paths = append(paths, p)
						} else if p := str(in, "path"); p != "" {
							paths = append(paths, p)
						} else {
							patch := str(in, "input")
							if patch == "" {
								patch = str(in, "patch")
							}
							paths = patchPaths(patch)
						}
						for _, p := range paths {
							ev := base
							ev.Kind, ev.Path = model.KindEdit, p
							id := str(blk, "id")
							edits[id] = append(edits[id], b.add("", ev))
						}
					}
				case "tool_result":
					isErr, _ := boolOf(blk, "is_error")
					if es := edits[str(blk, "tool_use_id")]; len(es) > 0 {
						for _, e := range es {
							e.drop = e.drop || isErr
						}
						continue
					}
					e := b.get(str(blk, "tool_use_id"))
					if e == nil {
						continue
					}
					e.ev.Exit = firstInt(blk, "exitCode", "exit_code")
					content := text(get(blk, "content"))
					if e.ev.Exit != nil && *e.ev.Exit != 0 {
						e.ev.StderrHead = norm.Head(content)
					} else {
						e.ev.StdoutHead = norm.Head(content)
					}
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no Droid session_start/message records")
	}
	return filterRepo(b.events(), repoRoot, since), nil
}
