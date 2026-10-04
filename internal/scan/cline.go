package scan

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Cline reads Cline SDK session artifacts (CLI, VS Code and desktop share
// them): $CLINE_SESSION_DATA_DIR, else ~/.cline/data/sessions, laid out as
// <sessionId>/<sessionId>.messages.json with the manifest <sessionId>.json
// beside it (subagent transcripts are <agentId>.messages.json in the root
// session's directory).
//
// The messages file is {version, sessionId, messages:[{role, ts, content:[...]}]}
// with Anthropic-style tool_use / tool_result blocks. The manifest carries
// cwd and started_at. run_commands takes {commands:[...]} and its result is a
// ToolOperationResult array, one {query, result, error, success} per command
// in input order. Exit status comes only from the notice Cline's own shell
// executor formats for a non-zero exit: error "Command exited with code N" (also
// the "[Command exited with code N]" first line of result). A successful
// command carries no exit code at all (success:true also covers a command
// moved to the background), so successes stay unknown and are dropped
// downstream. editor / apply_patch results with success:false or is_error are
// dropped.
//
// Verified from cline/cline sdk/packages/core (session-artifacts.ts,
// session-manifest.ts, session-data.ts, extensions/tools/definitions.ts,
// executors/bash.ts). The pre-SDK tasks/<id>/api_conversation_history.json
// layout is not read.
type Cline struct{}

func (Cline) ID() string { return "cline" }

func (c Cline) Discover(home string, env func(string) string) (Status, []Source) {
	dirs := roots(env, "CLINE_SESSION_DATA_DIR", filepath.Join(home, ".cline", "data", "sessions"))
	return discoverFiles(c.ID(), dirs, func(p string, _ fs.DirEntry) bool {
		return strings.HasSuffix(p, ".messages.json")
	})
}

var (
	clineExitRe  = regexp.MustCompile(`^\s*\[?Command exited with code (-?\d+)\]?`)
	clineNotice  = regexp.MustCompile(`^\s*\[(?:Command exited with code -?\d+|Command terminated[^\]]*|Command is still running[^\]]*)\]\s*\n?`)
	clineToolMap = map[string]model.Kind{
		"run_commands": model.KindShell,
		"editor":       model.KindEdit,
		"apply_patch":  model.KindEdit,
	}
)

func (c Cline) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	raw, err := os.ReadFile(src.Path)
	if err != nil {
		return nil, err
	}
	var doc obj
	if json.Unmarshal(raw, &doc) != nil {
		return nil, unrecognized("cline messages file is not a JSON object")
	}
	msgs, ok := doc["messages"].([]any)
	if !ok || doc["version"] == nil {
		return nil, unrecognized("no Cline messages array")
	}
	session := str(doc, "sessionId")
	if session == "" {
		session = strings.TrimSuffix(filepath.Base(src.Path), ".messages.json")
	}

	// The manifest sits in the root session's directory, named after it.
	dir := filepath.Dir(src.Path)
	var manifest obj
	_ = readJSON(filepath.Join(dir, filepath.Base(dir)+".json"), &manifest)
	cwd := str(manifest, "cwd")
	started := parseTime(manifest["started_at"])

	b := newBook()
	calls := map[string][]*entry{}
	for _, msg := range msgs {
		ts := parseTime(get(msg, "ts"))
		if ts.IsZero() {
			ts = started
		}
		base := model.Event{Agent: c.ID(), Session: session, TS: ts, Cwd: cwd}
		for _, blk := range list(msg, "content") {
			switch str(blk, "type") {
			case "tool_use":
				id := str(blk, "id")
				in := mapOf(blk, "input")
				switch toolKind(str(blk, "name"), clineToolMap) {
				case model.KindShell:
					for _, cmd := range clineCommands(in) {
						ev := base
						ev.Kind, ev.Command = model.KindShell, cmd
						calls[id] = append(calls[id], b.add("", ev))
					}
				case model.KindEdit:
					var paths []string
					if p := str(in, "path"); p != "" {
						paths = append(paths, p)
					} else if p := str(in, "file_path"); p != "" {
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
						calls[id] = append(calls[id], b.add("", ev))
					}
				}
			case "tool_result":
				es := calls[str(blk, "tool_use_id")]
				if len(es) == 0 {
					continue
				}
				isErr, _ := boolOf(blk, "is_error")
				results := clineResults(get(blk, "content"))
				if es[0].ev.Kind == model.KindEdit {
					failed := isErr
					for _, r := range results {
						if ok, has := boolOf(r, "success"); has && !ok {
							failed = true
						}
					}
					if failed {
						for _, e := range es {
							e.drop = true
						}
					}
					continue
				}
				for i, e := range es {
					if i >= len(results) {
						break
					}
					clineShellResult(&e.ev, results[i])
				}
			}
		}
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

// clineCommands normalizes the run_commands input shapes Cline accepts.
func clineCommands(in obj) []string {
	one := func(v any) string {
		switch c := v.(type) {
		case string:
			return strings.TrimSpace(c)
		case obj:
			argv := []any{str(c, "command")}
			argv = append(argv, list(c, "args")...)
			return shellCommand(argv)
		}
		return ""
	}
	var out []string
	switch c := in["commands"].(type) {
	case []any:
		for _, v := range c {
			if s := one(v); s != "" {
				out = append(out, s)
			}
		}
	case string, obj:
		if s := one(c); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		for _, k := range []string{"command", "cmd"} {
			if s := one(in[k]); s != "" {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// clineResults decodes a tool_result content: an array of
// ToolOperationResult objects, or one JSON-encoded as a string.
func clineResults(v any) []obj {
	if s, ok := v.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(s), &decoded) != nil {
			return nil
		}
		v = decoded
	}
	var out []obj
	switch c := v.(type) {
	case []any:
		for _, it := range c {
			if m, ok := it.(obj); ok {
				out = append(out, m)
			}
		}
	case obj:
		out = append(out, c)
	}
	return out
}

func clineShellResult(ev *model.Event, r obj) {
	res, _ := r["result"].(string)
	if code := matchInt(clineExitRe, str(r, "error")); code != nil {
		ev.Exit = code
	} else if code := matchInt(clineExitRe, res); code != nil {
		ev.Exit = code
	}
	body := clineNotice.ReplaceAllString(res, "")
	if ev.Exit != nil && *ev.Exit != 0 {
		ev.StderrHead = norm.Head(body)
	} else {
		ev.StdoutHead = norm.Head(body)
	}
}
