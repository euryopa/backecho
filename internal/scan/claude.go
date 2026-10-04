package scan

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Claude reads Claude Code transcripts: ~/.claude/projects/<slug>/<session>.jsonl.
//
// Each assistant line carries tool_use blocks; the matching user line carries
// a tool_result block with the same id and a top-level toolUseResult.
// Exit status, in order:
//  1. toolUseResult.exitCode when present.
//  2. A tool_result marked is_error whose text starts "Exit code N" — this is
//     how Claude Code reports a non-zero exit.
//  3. A tool_result not marked is_error whose toolUseResult is the Bash
//     result object (stdout/stderr) and not interrupted or backgrounded:
//     Claude Code only reports a Bash call as an error when the exit is
//     non-zero, so this is exit 0.
//
// Checked against real Claude Code 2.x transcripts. Interrupted, timed-out
// and backgrounded calls stay unknown.
//
// Anything else stays unknown.
type Claude struct{}

func (Claude) ID() string { return "claude" }

func (c Claude) Discover(home string, env func(string) string) (Status, []Source) {
	// CLAUDE_CONFIG_DIR replaces both defaults.
	var dirs []string
	for _, d := range roots(env, "CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"), filepath.Join(home, ".config", "claude")) {
		dirs = append(dirs, filepath.Join(d, "projects"))
	}
	return discoverFiles(c.ID(), dirs, hasExt(".jsonl"))
}

var claudeExitRe = regexp.MustCompile(`^\s*(?:Error:\s*)?Exit code (-?\d+)`)

func (c Claude) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	b := newBook()
	recognized := false
	_, _, err := scanJSONL(src.Path, func(m obj) {
		typ := str(m, "type")
		if typ != "assistant" && typ != "user" {
			return
		}
		if str(m, "sessionId") == "" && str(m, "uuid") == "" {
			return
		}
		recognized = true
		base := model.Event{
			Agent:   c.ID(),
			Session: str(m, "sessionId"),
			TS:      parseTime(m["timestamp"]),
			Cwd:     str(m, "cwd"),
			Branch:  str(m, "gitBranch"),
		}
		if base.Session == "" {
			base.Session = sessionFromPath(src.Path)
		}
		for _, blk := range list(m, "message", "content") {
			switch str(blk, "type") {
			case "tool_use":
				name := str(blk, "name")
				in := mapOf(blk, "input")
				switch toolKind(name, nil) {
				case model.KindShell:
					ev := base
					ev.Kind = model.KindShell
					ev.Command = str(in, "command")
					e := b.add(str(blk, "id"), ev)
					if bg, _ := boolOf(in, "run_in_background"); bg {
						e.drop = true
					}
				case model.KindEdit:
					ev := base
					ev.Kind = model.KindEdit
					ev.Path = str(in, "file_path")
					if ev.Path == "" {
						ev.Path = str(in, "notebook_path")
					}
					b.add(str(blk, "id"), ev)
				default:
					if strings.EqualFold(name, "NotebookEdit") {
						ev := base
						ev.Kind = model.KindEdit
						ev.Path = str(in, "notebook_path")
						b.add(str(blk, "id"), ev)
					}
				}
			case "tool_result":
				e := b.get(str(blk, "tool_use_id"))
				if e == nil {
					continue
				}
				isErr, _ := boolOf(blk, "is_error")
				content := text(get(blk, "content"))
				if e.ev.Kind == model.KindEdit {
					if isErr {
						e.drop = true
					}
					continue
				}
				claudeResult(&e.ev, m["toolUseResult"], isErr, content)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no Claude Code message records")
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

func claudeResult(ev *model.Event, tur any, isErr bool, content string) {
	r, isObj := tur.(obj)
	if isObj {
		ev.StdoutHead = norm.Head(str(r, "stdout"))
		ev.StderrHead = norm.Head(str(r, "stderr"))
		if p := str(r, "persistedOutputPath"); p != "" && ev.StdoutHead == "" {
			ev.StdoutHead = firstLineOf(p)
		}
		if code := firstInt(r, "exitCode", "exit_code"); code != nil {
			ev.Exit = code
			return
		}
	}
	if isErr {
		msg := content
		if s, ok := tur.(string); ok && s != "" {
			msg = s
		}
		if code := matchInt(claudeExitRe, msg); code != nil {
			ev.Exit = code
			if ev.StderrHead == "" {
				// The first line is "Exit code N"; the hint is what follows.
				rest := msg
				if i := strings.IndexByte(rest, '\n'); i >= 0 {
					rest = rest[i+1:]
				} else {
					rest = ""
				}
				ev.StderrHead = norm.Head(rest)
			}
		}
		return
	}
	if !isObj {
		return
	}
	if _, ok := r["stdout"]; !ok {
		return
	}
	if interrupted, _ := boolOf(r, "interrupted"); interrupted {
		return
	}
	if str(r, "backgroundTaskId") != "" || r["timedOutAfterMs"] != nil {
		return
	}
	ev.Exit = model.IntPtr(0)
}
