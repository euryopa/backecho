package scan

import (
	"io/fs"
	"path/filepath"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Grok reads Grok Build (xAI grok CLI) sessions:
// $GROK_HOME/sessions/<encoded-cwd>/<session-id>/updates.jsonl, else
// ~/.grok/sessions/... . summary.json beside it holds info.id and info.cwd,
// which is the cwd of the session's commands.
//
// updates.jsonl is the ACP session/update stream:
// {timestamp, method:"session/update" (or "_x.ai/session/update"),
// params:{sessionId, update:{sessionUpdate, ...}, _meta:{agentTimestampMs}}}.
// A tool call arrives as "tool_call" then one or more "tool_call_update"
// records with the same toolCallId; fields (kind, title, rawInput, locations,
// status, rawOutput) are merged in order. kind "execute" is a shell call with
// rawInput.command; kind "edit" is an edit.
//
// Exit status comes only from the final update's rawOutput when it is the
// typed Bash output ({"type":"Bash", exit_code, signal, timed_out, ...}) and
// the update's status is completed/failed. A streaming chunk (output_delta),
// a backgrounded or signalled or timed-out run, or a negative exit_code (Grok's
// "no code" sentinel) stays unknown. Edits whose final status is "failed", or
// whose rawOutput is a SearchReplace / ApplyPatch error variant, are dropped.
//
// Verified from xai-org/grok-build: docs/user-guide/17-sessions.md,
// xai-grok-shell session/persistence.rs (Summary.info.cwd),
// session/acp_conversion.rs and acp_session_impl/tool_dispatch.rs (status and
// rawOutput), xai-grok-tools types/output.rs (ToolOutput, BashOutput).
type Grok struct{}

func (Grok) ID() string { return "grok" }

func (g Grok) Discover(home string, env func(string) string) (Status, []Source) {
	var dirs []string
	for _, b := range roots(env, "GROK_HOME", filepath.Join(home, ".grok")) {
		dirs = append(dirs, filepath.Join(b, "sessions"))
	}
	return discoverFiles(g.ID(), dirs, func(p string, _ fs.DirEntry) bool {
		return filepath.Base(p) == "updates.jsonl"
	})
}

type grokCall struct {
	ts        time.Time
	kind      string
	title     string
	rawInput  obj
	locations []any
	status    string
	rawOutput obj
}

func (g Grok) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	dir := filepath.Dir(src.Path)
	var summary obj
	_ = readJSON(filepath.Join(dir, "summary.json"), &summary)
	session := str(summary, "info", "id")
	if session == "" {
		session = filepath.Base(dir)
	}
	cwd := str(summary, "info", "cwd")

	var order []string
	calls := map[string]*grokCall{}
	recognized := false
	_, _, err := scanJSONL(src.Path, func(m obj) {
		method := str(m, "method")
		if method != "session/update" && method != "_x.ai/session/update" {
			return
		}
		p := mapOf(m, "params")
		u := mapOf(p, "update")
		su := str(u, "sessionUpdate")
		if su == "" {
			return
		}
		recognized = true
		if su != "tool_call" && su != "tool_call_update" {
			return
		}
		id := str(u, "toolCallId")
		if id == "" {
			return
		}
		c := calls[id]
		if c == nil {
			c = &grokCall{}
			calls[id] = c
			order = append(order, id)
		}
		if c.ts.IsZero() {
			c.ts = parseTime(get(p, "_meta", "agentTimestampMs"))
			if c.ts.IsZero() {
				c.ts = parseTime(m["timestamp"])
			}
		}
		if v := str(u, "kind"); v != "" {
			c.kind = v
		}
		if v := str(u, "title"); v != "" {
			c.title = v
		}
		if v := mapOf(u, "rawInput"); v != nil {
			c.rawInput = v
		}
		if v := list(u, "locations"); len(v) > 0 {
			c.locations = v
		}
		status := str(u, "status")
		if status != "" {
			c.status = status
		}
		if out := mapOf(u, "rawOutput"); out != nil {
			if status == "completed" || status == "failed" {
				if _, partial := out["output_delta"]; !partial {
					c.rawOutput = out
				}
			} else if str(out, "type") != "Bash" {
				c.rawOutput = out
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no Grok session/update records")
	}

	b := newBook()
	for _, id := range order {
		c := calls[id]
		base := model.Event{Agent: g.ID(), Session: session, TS: c.ts, Cwd: cwd}
		outType := str(c.rawOutput, "type")
		switch {
		case c.kind == "execute" || outType == "Bash":
			ev := base
			ev.Kind = model.KindShell
			ev.Command = str(c.rawInput, "command")
			if ev.Command == "" {
				ev.Command = str(c.rawOutput, "command")
			}
			if ev.Command == "" {
				continue
			}
			if ev.Cwd == "" {
				ev.Cwd = str(c.rawInput, "cwd")
			}
			grokBash(&ev, c)
			b.add(id, ev)
		case c.kind == "edit":
			if c.status == "failed" {
				continue
			}
			paths, ok := grokEditPaths(c)
			if !ok {
				continue
			}
			for _, p := range paths {
				ev := base
				ev.Kind, ev.Path = model.KindEdit, p
				b.add("", ev)
			}
		}
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

func grokBash(ev *model.Event, c *grokCall) {
	out := c.rawOutput
	if str(out, "type") != "Bash" || (c.status != "completed" && c.status != "failed") {
		return
	}
	head := norm.Head(str(out, "output_for_prompt"))
	if timedOut, _ := boolOf(out, "timed_out"); timedOut || out["signal"] != nil {
		ev.StdoutHead = head
		return
	}
	code := intOf(out, "exit_code")
	if code == nil || *code < 0 {
		ev.StdoutHead = head
		return
	}
	ev.Exit = code
	if *code != 0 {
		ev.StderrHead = head
	} else {
		ev.StdoutHead = head
	}
}

// grokEditPaths lists the files an edit touched; ok is false when the typed
// output says the edit was not applied.
func grokEditPaths(c *grokCall) ([]string, bool) {
	out := c.rawOutput
	switch str(out, "type") {
	case "SearchReplace":
		applied := mapOf(out, "EditsApplied")
		if applied == nil {
			return nil, false
		}
		if p := str(applied, "absolute_path"); p != "" {
			return []string{p}, true
		}
	case "ApplyPatch":
		success := mapOf(out, "Success")
		if success == nil {
			return nil, false
		}
		var paths []string
		for _, f := range list(success, "files") {
			if p := str(f, "path"); p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) > 0 {
			return paths, true
		}
	}
	var paths []string
	for _, l := range c.locations {
		if p := str(l, "path"); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		for _, k := range []string{"file_path", "path"} {
			if p := str(c.rawInput, k); p != "" {
				paths = append(paths, p)
				break
			}
		}
	}
	if len(paths) == 0 {
		paths = patchPaths(str(c.rawInput, "input"))
	}
	return paths, len(paths) > 0
}
