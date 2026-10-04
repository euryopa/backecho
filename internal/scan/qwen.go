package scan

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Qwen reads Qwen Code chat recordings. Current versions write
// <runtime>/projects/<sanitized-cwd>/chats/<sessionId>.jsonl; versions that
// still followed Gemini CLI wrote <root>/tmp/<projectHash>/chats/*.json(l)
// in the Gemini shape, which is parsed by the Gemini reader. <root> is
// $QWEN_DATA_DIR, else $QWEN_RUNTIME_DIR and $QWEN_HOME (config/storage.ts),
// else ~/.qwen.
//
// Verified against QwenLM/qwen-code packages/core/src/services/
// chatRecordingService.ts (ChatRecord) and tools/shell.ts. Every JSONL line
// is a ChatRecord {uuid, parentUuid, sessionId, timestamp, type, cwd,
// gitBranch, message, toolCallResult}. type "assistant" records carry
// message.parts[].functionCall {id, name, args}; type "tool_result" records
// carry message.parts[].functionResponse {id, name, response:{output|error}}
// and toolCallResult {callId, status, error, resultDisplay}. The cwd is the
// recorded project root of each record; run_shell_command's absolute
// directory argument overrides it.
//
// Exit status comes only from the "Exit Code: N" line run_shell_command
// writes into its llmContent on every completed command (shell.ts: "Command:",
// "Directory:", "Output:", "Error:", "Exit Code:", "Signal:", "Process Group
// PGID:"); "Exit Code: (none)", a cancelled or timed-out command, and a
// background start stay unknown. Edits (edit, write_file) whose result is an
// error are dropped.
type Qwen struct{}

func (Qwen) ID() string { return "qwen" }

func (q Qwen) Discover(home string, env func(string) string) (Status, []Source) {
	bases := roots(env, "QWEN_DATA_DIR")
	if len(bases) == 0 {
		bases = append(roots(env, "QWEN_RUNTIME_DIR"), roots(env, "QWEN_HOME")...)
	}
	if len(bases) == 0 {
		bases = []string{filepath.Join(home, ".qwen")}
	}
	var dirs []string
	for _, b := range bases {
		dirs = append(dirs, filepath.Join(b, "projects"), filepath.Join(b, "tmp"))
	}
	return discoverFiles(q.ID(), dirs, func(p string, d fs.DirEntry) bool { return geminiChatFile(p, d) })
}

var qwenTools = map[string]model.Kind{
	"run_shell_command": model.KindShell,
	"write_file":        model.KindEdit,
	"edit":              model.KindEdit,
	"replace":           model.KindEdit,
}

func (q Qwen) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	if strings.HasSuffix(src.Path, ".json") {
		return geminiParseFile(q.ID(), src.Path, repoRoot, since)
	}
	b := newBook()
	recognized, legacy := false, false
	_, _, err := scanJSONL(src.Path, func(m obj) {
		if str(m, "uuid") == "" || str(m, "sessionId") == "" {
			if str(m, "projectHash") != "" {
				legacy = true
			}
			return
		}
		typ := str(m, "type")
		if typ != "assistant" && typ != "tool_result" && typ != "user" && typ != "system" {
			return
		}
		recognized = true
		base := model.Event{
			Agent:   q.ID(),
			Session: str(m, "sessionId"),
			TS:      parseTime(m["timestamp"]),
			Cwd:     str(m, "cwd"),
			Branch:  str(m, "gitBranch"),
		}
		switch typ {
		case "assistant":
			for _, part := range list(m, "message", "parts") {
				fc := mapOf(part, "functionCall")
				if fc == nil {
					continue
				}
				a := args(fc["args"])
				ev := base
				switch toolKind(str(fc, "name"), qwenTools) {
				case model.KindShell:
					if bg, _ := boolOf(a, "is_background"); bg {
						continue
					}
					ev.Kind = model.KindShell
					ev.Command = strings.TrimSpace(str(a, "command"))
					ev.Cwd = geminiCwd(base.Cwd, str(a, "directory"))
					b.add(str(fc, "id"), ev)
				case model.KindEdit:
					ev.Kind = model.KindEdit
					ev.Path = str(a, "file_path")
					if ev.Path == "" {
						continue
					}
					b.add(str(fc, "id"), ev)
				}
			}
		case "tool_result":
			tcr := mapOf(m, "toolCallResult")
			for _, part := range list(m, "message", "parts") {
				fr := mapOf(part, "functionResponse")
				if fr == nil {
					continue
				}
				id := str(fr, "id")
				if id == "" {
					id = str(tcr, "callId")
				}
				e := b.get(id)
				if e == nil {
					continue
				}
				out, errText := geminiResult([]any{part})
				failed := errText != "" || str(tcr, "status") == "error" || str(tcr, "status") == "cancelled"
				if e.ev.Kind == model.KindEdit {
					if failed {
						e.drop = true
					}
					continue
				}
				if str(tcr, "status") == "cancelled" {
					continue
				}
				geminiShellResult(&e.ev, out+"\n"+errText)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		if legacy {
			return geminiParseFile(q.ID(), src.Path, repoRoot, since)
		}
		return nil, unrecognized("no Qwen Code chat records")
	}
	return filterRepo(b.events(), repoRoot, since), nil
}
