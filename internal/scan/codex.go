package scan

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Codex reads Codex CLI rollouts: $CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl.
//
// Lines are {timestamp, type, payload}. session_meta and turn_context carry
// the cwd; response_item function_call / local_shell_call / custom_tool_call
// carry the call; function_call_output and event_msg exec_command_end carry
// the exit status. Exit status comes from, in order: exec_command_end
// exit_code, the JSON output's metadata.exit_code, or the header line Codex
// itself writes into the output ("Exit code: N", "Process exited with code N").
// A process still running ("Process running with session ID") is unknown.
type Codex struct{}

func (Codex) ID() string { return "codex" }

func (c Codex) Discover(home string, env func(string) string) (Status, []Source) {
	bases := roots(env, "CODEX_HOME", filepath.Join(home, ".codex"))
	var dirs []string
	for _, b := range bases {
		dirs = append(dirs, filepath.Join(b, "sessions"), filepath.Join(b, "archived_sessions"))
	}
	return discoverFiles(c.ID(), dirs, func(p string, _ fs.DirEntry) bool {
		return strings.HasSuffix(p, ".jsonl") && strings.HasPrefix(filepath.Base(p), "rollout-")
	})
}

var (
	codexProcExitRe = regexp.MustCompile(`(?m)^Process exited with code (-?\d+)\s*$`)
	codexCwdRe      = regexp.MustCompile(`<cwd>([^<]+)</cwd>`)
)

var codexTools = map[string]model.Kind{
	"shell":             model.KindShell,
	"container.exec":    model.KindShell,
	"exec_command":      model.KindShell,
	"shell_command":     model.KindShell,
	"local_shell":       model.KindShell,
	"apply_patch":       model.KindEdit,
	"write_stdin":       "",
	"update_plan":       "",
	"view_image":        "",
	"unified_exec":      model.KindShell,
	"container.execute": model.KindShell,
}

func (c Codex) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	b := newBook()
	session := sessionFromPath(src.Path)
	var cwd, branch string
	recognized := false

	handle := func(ts time.Time, p obj) {
		base := model.Event{Agent: c.ID(), Session: session, TS: ts, Cwd: cwd, Branch: branch}
		switch str(p, "type") {
		case "function_call", "custom_tool_call", "local_shell_call":
			name := str(p, "name")
			if str(p, "type") == "local_shell_call" {
				name = "local_shell"
			}
			kind := toolKind(name, codexTools)
			id := str(p, "call_id")
			if id == "" {
				id = str(p, "id")
			}
			switch kind {
			case model.KindShell:
				a := args(p["arguments"])
				if a == nil {
					a = mapOf(p, "action")
				}
				cmd := shellCommand(a["command"])
				if cmd == "" {
					cmd = shellCommand(a["cmd"])
				}
				ev := base
				ev.Kind = model.KindShell
				ev.Command = cmd
				if wd := str(a, "workdir"); wd != "" {
					ev.Cwd = wd
				} else if wd := str(a, "working_directory"); wd != "" {
					ev.Cwd = wd
				}
				// apply_patch can arrive as a shell call: ["apply_patch", "*** Begin Patch..."].
				if strings.HasPrefix(cmd, "apply_patch ") || strings.Contains(cmd, "*** Begin Patch") {
					for _, path := range patchPaths(cmd) {
						pe := base
						pe.Kind, pe.Path = model.KindEdit, path
						b.add("", pe)
					}
					return
				}
				b.add(id, ev)
			case model.KindEdit:
				patch := str(p, "input")
				if patch == "" {
					patch = str(args(p["arguments"]), "input")
				}
				for _, path := range patchPaths(patch) {
					ev := base
					ev.Kind, ev.Path = model.KindEdit, path
					b.add("", ev)
				}
			}
		case "function_call_output", "custom_tool_call_output":
			e := b.get(str(p, "call_id"))
			if e == nil || e.ev.Kind != model.KindShell || e.ev.Exit != nil {
				return
			}
			codexOutput(&e.ev, p["output"])
		case "exec_command_end":
			e := b.get(str(p, "call_id"))
			if e == nil {
				return
			}
			if code := intOf(p, "exit_code"); code != nil {
				e.ev.Exit = code
			}
			if s := norm.Head(str(p, "stderr")); s != "" {
				e.ev.StderrHead = s
			}
			if s := norm.Head(str(p, "stdout")); s != "" && e.ev.StdoutHead == "" {
				e.ev.StdoutHead = s
			}
		case "patch_apply_end":
			if ok, _ := boolOf(p, "success"); !ok {
				return
			}
			for path := range mapOf(p, "changes") {
				ev := base
				ev.Kind, ev.Path = model.KindEdit, path
				b.add("", ev)
			}
		case "message":
			if str(p, "role") == "user" {
				for _, blk := range list(p, "content") {
					if m := codexCwdRe.FindStringSubmatch(str(blk, "text")); m != nil {
						cwd = strings.TrimSpace(m[1])
					}
				}
			}
		}
	}

	_, _, err := scanJSONL(src.Path, func(m obj) {
		ts := parseTime(m["timestamp"])
		p := mapOf(m, "payload")
		switch str(m, "type") {
		case "session_meta":
			recognized = true
			if id := str(p, "id"); id != "" {
				session = id
			}
			if v := str(p, "cwd"); v != "" {
				cwd = v
			}
			if v := str(p, "git", "branch"); v != "" {
				branch = v
			}
		case "turn_context":
			recognized = true
			if v := str(p, "cwd"); v != "" {
				cwd = v
			}
		case "response_item", "event_msg":
			recognized = true
			handle(ts, p)
		case "":
			// Old rollouts start with an unenveloped session header.
			if str(m, "id") != "" && m["instructions"] != nil {
				recognized = true
				session = str(m, "id")
			}
		case "message", "function_call", "function_call_output", "local_shell_call", "custom_tool_call", "custom_tool_call_output":
			// Rollouts before mid-2025 wrote items without an envelope.
			if p == nil {
				recognized = true
				handle(ts, m)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no Codex rollout records")
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

func codexOutput(ev *model.Event, out any) {
	s, _ := out.(string)
	if s == "" {
		s = text(out)
	}
	var j obj
	if strings.HasPrefix(strings.TrimSpace(s), "{") && json.Unmarshal([]byte(s), &j) == nil {
		if code := intOf(j, "metadata", "exit_code"); code != nil {
			ev.Exit = code
		}
		ev.StdoutHead = norm.Head(str(j, "output"))
		return
	}
	if strings.Contains(s, "Process running with session ID") {
		return
	}
	if code := matchInt(exitLineRe, s); code != nil {
		ev.Exit = code
	} else if code := matchInt(codexProcExitRe, s); code != nil {
		ev.Exit = code
	}
	if i := strings.Index(s, "Output:\n"); i >= 0 {
		ev.StdoutHead = norm.Head(s[i+len("Output:\n"):])
	}
}
