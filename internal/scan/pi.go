package scan

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Pi reads pi-agent (badlogic/pi-mono, packages/coding-agent) and its fork
// omp (can1357/oh-my-pi) session trees:
// ~/.pi/agent/sessions/<encoded-cwd>/<ts>_<id>.jsonl and
// ~/.omp/agent/sessions/... . PI_AGENT_DIR or PI_CODING_AGENT_DIR (the
// variable pi itself reads) replaces both agent dirs, sessions under it;
// PI_CODING_AGENT_SESSION_DIR names a sessions dir directly.
//
// The first line is the header {type:"session", id, timestamp, cwd}. Later
// lines are {type:"message", id, parentId, timestamp, message}; an assistant
// message holds {type:"toolCall", id, name, arguments} blocks and the result
// is a {role:"toolResult", toolCallId, toolName, content, details, isError}
// message. A user "!cmd" is a {role:"bashExecution", command, exitCode,
// cancelled} message.
//
// Exit status, in order:
//  1. details.exitCode (omp writes it on a non-zero exit; OpenClaw's exec
//     writes it with details.status "completed"/"failed"; a "running" or
//     approval status is unknown).
//  2. The status line the bash tool itself appends, "Command exited with
//     code N" (pi and omp, isError true).
//  3. A built-in "bash" result with isError false and no async/timeout
//     details: pi's bash tool (core/tools/bash.ts) returns isError only for a
//     non-zero exit and throws on abort, timeout or a missing exit code, so
//     this is exit 0 — the same tool-semantics rule as the Claude adapter.
//  4. bashExecution.exitCode when not cancelled.
//
// Anything else is unknown. Edits whose result isError are dropped. The cwd
// is the header cwd, or the call's own cwd/workdir argument resolved
// against it.
type Pi struct{}

func (Pi) ID() string { return "pi" }

func (p Pi) Discover(home string, env func(string) string) (Status, []Source) {
	var dirs []string
	for _, key := range []string{"PI_AGENT_DIR", "PI_CODING_AGENT_DIR"} {
		if strings.TrimSpace(env(key)) == "" {
			continue
		}
		for _, d := range roots(env, key) {
			dirs = append(dirs, filepath.Join(d, "sessions"))
		}
	}
	if strings.TrimSpace(env("PI_CODING_AGENT_SESSION_DIR")) != "" {
		dirs = append(dirs, roots(env, "PI_CODING_AGENT_SESSION_DIR")...)
	}
	if len(dirs) == 0 {
		dirs = []string{
			filepath.Join(home, ".pi", "agent", "sessions"),
			filepath.Join(home, ".omp", "agent", "sessions"),
		}
	}
	return discoverFiles(p.ID(), dirs, hasExt(".jsonl"))
}

func (p Pi) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return piParseSession(p.ID(), src.Path, repoRoot, since)
}

var (
	piExitRe = regexp.MustCompile(`(?m)^Command exited with code (-?\d+)\s*$`)

	piTools = map[string]model.Kind{
		"bash":        model.KindShell,
		"exec":        model.KindShell, // OpenClaw
		"powershell":  model.KindShell,
		"process":     "", // OpenClaw: polls a backgrounded exec
		"edit":        model.KindEdit,
		"write":       model.KindEdit,
		"apply_patch": model.KindEdit,
		"ast_edit":    "",
	}
)

// piParseSession parses one pi-format session JSONL (pi, omp, OpenClaw).
func piParseSession(agent, path, repoRoot string, since time.Time) ([]model.Event, error) {
	b := newBook()
	extra := map[string][]*entry{} // apply_patch: further files of one call
	recognized := false
	session := sessionFromPath(path)
	cwd := ""
	_, _, err := scanJSONL(path, func(m obj) {
		switch str(m, "type") {
		case "session":
			if str(m, "id") == "" {
				return
			}
			recognized = true
			session = str(m, "id")
			cwd = str(m, "cwd")
			return
		case "message":
		default:
			return
		}
		msg := mapOf(m, "message")
		if msg == nil || !recognized {
			return
		}
		ts := parseTime(m["timestamp"])
		if ts.IsZero() {
			ts = parseTime(msg["timestamp"])
		}
		base := model.Event{Agent: agent, Session: session, TS: ts, Cwd: cwd}
		switch str(msg, "role") {
		case "assistant":
			for _, blk := range list(msg, "content") {
				if str(blk, "type") != "toolCall" {
					continue
				}
				id := str(blk, "id")
				name := str(blk, "name")
				in := args(get(blk, "arguments"))
				switch toolKind(name, piTools) {
				case model.KindShell:
					ev := base
					ev.Kind = model.KindShell
					ev.Command = strings.TrimSpace(str(in, "command"))
					if ev.Command == "" {
						continue
					}
					ev.Cwd = piResolve(cwd, piFirstStr(in, "cwd", "workdir"))
					b.add(id, ev)
				case model.KindEdit:
					var paths []string
					if p := piFirstStr(in, "path", "file_path"); p != "" {
						paths = []string{p}
					} else {
						paths = patchPaths(piFirstStr(in, "input", "patch"))
					}
					for i, p := range paths {
						ev := base
						ev.Kind = model.KindEdit
						ev.Path = p
						if i == 0 {
							b.add(id, ev)
						} else {
							extra[id] = append(extra[id], b.add("", ev))
						}
					}
				}
			}
		case "toolResult":
			id := str(msg, "toolCallId")
			e := b.get(id)
			if e == nil {
				return
			}
			isErr, _ := boolOf(msg, "isError")
			if e.ev.Kind == model.KindEdit {
				if isErr {
					e.drop = true
					for _, x := range extra[id] {
						x.drop = true
					}
				}
				return
			}
			piResult(&e.ev, msg, isErr)
		case "bashExecution":
			ev := base
			ev.Kind = model.KindShell
			ev.Command = strings.TrimSpace(str(msg, "command"))
			if ev.Command == "" {
				return
			}
			out := str(msg, "output")
			ev.StdoutHead = norm.Head(out)
			if cancelled, _ := boolOf(msg, "cancelled"); !cancelled {
				ev.Exit = intOf(msg, "exitCode")
			}
			b.add("", ev)
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no pi session header")
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

func piResult(ev *model.Event, msg obj, isErr bool) {
	content := text(get(msg, "content"))
	details := mapOf(msg, "details")
	body := strings.TrimSpace(piExitRe.ReplaceAllString(content, ""))
	if agg := str(details, "aggregated"); agg != "" {
		body = agg
	}
	if c := str(details, "cwd"); c != "" && filepath.IsAbs(c) {
		ev.Cwd = c
	}
	head := norm.Head(body)
	if isErr {
		ev.StderrHead = head
	} else {
		ev.StdoutHead = head
	}
	if details != nil {
		if st := str(details, "status"); st != "" && st != "completed" && st != "failed" {
			return // running, approval-pending, ...: no exit yet
		}
		if code := intOf(details, "exitCode"); code != nil {
			ev.Exit = code
			return
		}
	}
	if isErr {
		ev.Exit = matchInt(piExitRe, content)
		return
	}
	if strings.ToLower(str(msg, "toolName")) != "bash" {
		return
	}
	if details != nil {
		if details["async"] != nil || details["timedOut"] != nil || details["status"] != nil {
			return
		}
	}
	ev.Exit = model.IntPtr(0)
}

// piResolve makes a call's own working directory absolute.
func piResolve(sessionCwd, dir string) string {
	switch {
	case dir == "":
		return sessionCwd
	case filepath.IsAbs(dir):
		return filepath.Clean(dir)
	case sessionCwd != "":
		return filepath.Join(sessionCwd, dir)
	}
	return ""
}

func piFirstStr(m obj, keys ...string) string {
	for _, k := range keys {
		if s := strings.TrimSpace(str(m, k)); s != "" {
			return s
		}
	}
	return ""
}
