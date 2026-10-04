package scan

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Copilot reads GitHub Copilot CLI session logs:
// $COPILOT_HOME/session-state/<session-id>/events.jsonl (default ~/.copilot).
//
// Each line is {type, data, id, timestamp, parentId}. session.start (and
// session.resume) carry data.sessionId and data.context.cwd.
// tool.execution_start carries data.toolCallId, data.toolName and
// data.arguments (bash: command, mode; edit/create: path).
// tool.execution_complete carries the same toolCallId, data.success and
// data.result {content, detailedContent, contents}.
//
// Exit status comes from, in order:
//  1. a structured result.contents[] block of type "terminal" with exitCode
//     (the SDK's ToolExecutionCompleteContentTerminal);
//  2. the marker the bash tool itself appends to its output as the last
//     line: "<exited with exit code N>" or "<shellId: X completed with exit
//     code N>".
//
// data.success is never read as an exit status. An edit whose completion has
// success:false is dropped.
//
// Verified from public sources: event envelope and session.start/tool event
// shapes (gist RockNoggin/8cc87f640ce5e3d284298a6db21f7523, github/copilot-cli
// issues #3366, #2649), the "<exited with exit code N>" marker
// (github/copilot-cli#2402) and the "<shellId: N completed with exit code N>"
// marker plus the terminal contents type (github/copilot-sdk#1803).
type Copilot struct{}

func (Copilot) ID() string { return "copilot" }

func (c Copilot) Discover(home string, env func(string) string) (Status, []Source) {
	var dirs []string
	for _, b := range roots(env, "COPILOT_HOME", filepath.Join(home, ".copilot")) {
		dirs = append(dirs, filepath.Join(b, "session-state"))
	}
	return discoverFiles(c.ID(), dirs, func(p string, _ fs.DirEntry) bool {
		return filepath.Base(p) == "events.jsonl"
	})
}

var (
	copilotExitRe  = regexp.MustCompile(`^<exited with exit code (-?\d+)>$`)
	copilotShellRe = regexp.MustCompile(`^<shellId: [^\s>]+ completed with exit code (-?\d+)>$`)
)

var copilotTools = map[string]model.Kind{
	"bash":       model.KindShell,
	"powershell": model.KindShell,
	"edit":       model.KindEdit,
	"create":     model.KindEdit,
}

func (c Copilot) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	b := newBook()
	session := filepath.Base(filepath.Dir(src.Path))
	var cwd string
	recognized := false
	_, _, err := scanJSONL(src.Path, func(m obj) {
		typ := str(m, "type")
		data := mapOf(m, "data")
		if typ == "" || data == nil {
			return
		}
		ts := parseTime(m["timestamp"])
		switch typ {
		case "session.start", "session.resume":
			recognized = true
			if s := str(data, "sessionId"); s != "" {
				session = s
			}
			if d := str(data, "context", "cwd"); d != "" {
				cwd = d
			}
		case "tool.execution_start":
			recognized = true
			id := str(data, "toolCallId")
			a := args(data["arguments"])
			base := model.Event{Agent: c.ID(), Session: session, TS: ts, Cwd: cwd}
			switch toolKind(str(data, "toolName"), copilotTools) {
			case model.KindShell:
				ev := base
				ev.Kind = model.KindShell
				ev.Command = shellCommand(a["command"])
				if ev.Command == "" {
					return
				}
				b.add(id, ev)
			case model.KindEdit:
				ev := base
				ev.Kind = model.KindEdit
				ev.Path = str(a, "path")
				if ev.Path == "" {
					return
				}
				b.add(id, ev)
			}
		case "tool.execution_complete":
			recognized = true
			e := b.get(str(data, "toolCallId"))
			if e == nil {
				return
			}
			if e.ev.Kind == model.KindEdit {
				if ok, known := boolOf(data, "success"); known && !ok {
					e.drop = true
				}
				return
			}
			copilotResult(&e.ev, mapOf(data, "result"))
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no Copilot CLI session or tool events")
	}
	// Sessions recorded before the session id was known keep one key.
	evs := b.events()
	for i := range evs {
		evs[i].Session = session
	}
	return filterRepo(evs, repoRoot, since), nil
}

func copilotResult(ev *model.Event, r obj) {
	if r == nil {
		return
	}
	for _, blk := range list(r, "contents") {
		if str(blk, "type") != "terminal" {
			continue
		}
		if code := intOf(blk, "exitCode"); code != nil {
			ev.Exit = code
		}
		if d := str(blk, "cwd"); d != "" && ev.Cwd == "" {
			ev.Cwd = d
		}
		if s := str(blk, "text"); s != "" && ev.StdoutHead == "" {
			ev.StdoutHead = norm.Head(s)
		}
	}
	content := str(r, "content")
	if content == "" {
		content = str(r, "detailedContent")
	}
	body, code := copilotSplitMarker(content)
	if ev.Exit == nil {
		ev.Exit = code
	}
	if ev.StdoutHead == "" {
		ev.StdoutHead = norm.Head(body)
	}
}

// copilotSplitMarker removes the trailing exit marker the bash tool writes
// and returns the output before it and the code, nil when absent.
func copilotSplitMarker(content string) (string, *int) {
	trimmed := strings.TrimRight(content, " \t\r\n")
	i := strings.LastIndexByte(trimmed, '\n')
	last := strings.TrimSpace(trimmed[i+1:])
	for _, re := range []*regexp.Regexp{copilotExitRe, copilotShellRe} {
		if code := matchInt(re, last); code != nil {
			if i < 0 {
				return "", code
			}
			return trimmed[:i], code
		}
	}
	return content, nil
}
