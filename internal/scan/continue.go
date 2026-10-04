package scan

import (
	"encoding/json"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Continue reads Continue sessions (VS Code / JetBrains extension and the cn
// CLI): $CONTINUE_GLOBAL_DIR/sessions/<sessionId>.json, else
// ~/.continue/sessions. sessions.json there is the index and is skipped.
//
// A session is {sessionId, workspaceDirectory, history:[{message,
// toolCallStates:[{toolCallId, toolCall{function{name, arguments}}, status,
// parsedArgs, output:[{name, content, status}]}]}]}. workspaceDirectory is a
// file:// URI from the IDE and a plain path from the CLI; it is the cwd of
// every command (the IDE runs in the first workspace dir, the CLI in
// process.cwd()). Records carry no per-message timestamp.
//
// Exit status comes only from text the terminal tool itself formats for a
// non-zero exit: the IDE tool's output status "Command failed with exit code
// N", or the CLI Bash tool's "Error executing tool Bash: Error (exit code N)".
// "Command completed" is also written for a signal-killed process, so a
// success is never read as exit 0 and stays unknown. Edits
// (edit_existing_file, single_find_and_replace, multi_edit, create_new_file;
// CLI Edit/Write/MultiEdit) are kept only with status "done".
//
// Verified from continuedev/continue core/index.d.ts, core/util/paths.ts,
// core/tools/builtIn.ts, core/tools/implementations/runTerminalCommand.ts and
// extensions/cli/src (tools/runTerminalCommand.ts, stream/streamChatResponse.helpers.ts).
type Continue struct{}

func (Continue) ID() string { return "continue" }

func (c Continue) Discover(home string, env func(string) string) (Status, []Source) {
	var dirs []string
	for _, b := range roots(env, "CONTINUE_GLOBAL_DIR", filepath.Join(home, ".continue")) {
		dirs = append(dirs, filepath.Join(b, "sessions"))
	}
	return discoverFiles(c.ID(), dirs, func(p string, _ fs.DirEntry) bool {
		return strings.HasSuffix(p, ".json") && filepath.Base(p) != "sessions.json" &&
			filepath.Base(filepath.Dir(p)) == "sessions"
	})
}

var (
	continueStatusExitRe = regexp.MustCompile(`(?:^|: )Command failed with exit code (-?\d+)`)
	continueCLIExitRe    = regexp.MustCompile(`^Error executing tool \w+: Error \(exit code (-?\d+)\)`)
	continueTools        = map[string]model.Kind{
		"run_terminal_command":            model.KindShell,
		"builtin_run_terminal_command":    model.KindShell,
		"edit_existing_file":              model.KindEdit,
		"builtin_edit_existing_file":      model.KindEdit,
		"single_find_and_replace":         model.KindEdit,
		"builtin_single_find_and_replace": model.KindEdit,
		"multi_edit":                      model.KindEdit,
		"builtin_multi_edit":              model.KindEdit,
		"create_new_file":                 model.KindEdit,
		"builtin_create_new_file":         model.KindEdit,
	}
)

func (c Continue) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	raw, err := os.ReadFile(src.Path)
	if err != nil {
		return nil, err
	}
	var doc obj
	if json.Unmarshal(raw, &doc) != nil {
		return nil, unrecognized("continue session is not a JSON object")
	}
	history, ok := doc["history"].([]any)
	session := str(doc, "sessionId")
	if !ok || session == "" {
		return nil, unrecognized("no Continue sessionId/history")
	}
	base := model.Event{Agent: c.ID(), Session: session, Cwd: continuePath(str(doc, "workspaceDirectory"))}

	b := newBook()
	for _, item := range history {
		if str(item, "message", "role") != "assistant" {
			continue
		}
		states := list(item, "toolCallStates")
		if len(states) == 0 {
			// No state recorded: the calls were never run or never reported.
			for _, tc := range list(item, "message", "toolCalls") {
				continueCall(b, base, obj{"toolCall": tc})
			}
			continue
		}
		for _, st := range states {
			if m, ok := st.(obj); ok {
				continueCall(b, base, m)
			}
		}
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

func continueCall(b *book, base model.Event, st obj) {
	name := str(st, "toolCall", "function", "name")
	a := mapOf(st, "parsedArgs")
	if a == nil {
		a = args(get(st, "toolCall", "function", "arguments"))
	}
	status := str(st, "status")
	switch toolKind(name, continueTools) {
	case model.KindShell:
		ev := base
		ev.Kind = model.KindShell
		ev.Command = strings.TrimSpace(str(a, "command"))
		if ev.Command == "" {
			return
		}
		for _, o := range list(st, "output") {
			content := str(o, "content")
			if code := matchInt(continueStatusExitRe, str(o, "status")); code != nil {
				ev.Exit = code
				ev.StderrHead = norm.Head(content)
			} else if code := matchInt(continueCLIExitRe, content); code != nil {
				ev.Exit = code
				rest := continueCLIExitRe.ReplaceAllString(content, "")
				ev.StderrHead = norm.Head(strings.TrimPrefix(rest, ":"))
			} else if ev.StdoutHead == "" {
				ev.StdoutHead = norm.Head(content)
			}
		}
		b.add(str(st, "toolCallId"), ev)
	case model.KindEdit:
		if status != "done" {
			return
		}
		p := str(a, "filepath")
		if p == "" {
			p = str(a, "file_path")
		}
		if p == "" {
			return
		}
		ev := base
		ev.Kind, ev.Path = model.KindEdit, continuePath(p)
		b.add(str(st, "toolCallId"), ev)
	}
}

// continuePath turns a file:// URI into a local path; a plain path is
// returned as is, any other URI scheme (vscode-remote://...) is unknown.
func continuePath(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := u.Path
	// file:///C:/x -> C:/x
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}
