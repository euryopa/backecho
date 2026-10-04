package scan

import (
	"crypto/md5"
	"encoding/hex"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Kimi reads Kimi Code CLI sessions and the legacy Kimi CLI (Python) ones.
//
// Kimi Code (MoonshotAI/kimi-code, docs/en/configuration/data-locations.md,
// packages/agent-core-v2): $KIMI_CODE_HOME (default ~/.kimi-code)/sessions/
// <workDirKey>/<sessionId>/agents/<agent>/wire.jsonl. The journal opens with
// {"type":"metadata","protocol_version",...}; every other line is
// {"type", ...payload, "time"(unix ms)}. The cwd comes from config.update /
// profile.bind ("cwd", or "environmentDisclosure.cwd" in newer protocols),
// else from the session_index.jsonl line {sessionId, sessionDir, workDir}.
// Tool calls arrive as context.append_loop_event {event:{type:"tool.call",
// toolCallId, name, args}} and results as {event:{type:"tool.result",
// toolCallId, result:{output, isError}}}; context.append_message with role
// assistant toolCalls [{id, name, arguments}] / role tool {toolCallId,
// content, isError} is read too. The Bash tool (bashTool.ts) ends a non-zero
// result with the line "Command failed with exit code: N." and returns
// exactly "Command executed successfully." when exit 0 printed nothing.
// Those are the only exit sources: a success that printed output carries no
// exit marker and stays unknown (dropped downstream), as do timeouts,
// interrupts and background starts. Bash's cwd argument overrides the
// session cwd. Write/Edit (path) results with isError are dropped.
//
// Legacy Kimi CLI (MoonshotAI/kimi-cli, share.py, metadata.py, soul/
// message.py, tools/shell): $KIMI_SHARE_DIR (default ~/.kimi)/sessions/
// <md5(workDir)>/<sessionId>/context.jsonl, OpenAI-shaped messages
// {role, content, tool_calls:[{id, function:{name, arguments}}]} and
// {role:"tool", tool_call_id, content}. The Shell tool always sets a message
// that becomes the first content part: "<system>Command executed
// successfully.</system>" (exit 0) or "<system>ERROR: Command failed with
// exit code: N.</system>". The file has no cwd and no timestamps; the cwd is
// the kimi.json work_dirs entry whose md5(path) names the session bucket
// (that file is Kimi's own record of the work dir for that bucket).
// WriteFile/StrReplaceFile (path) results starting "<system>ERROR" are
// dropped.
type Kimi struct{}

func (Kimi) ID() string { return "kimi" }

func (k Kimi) Discover(home string, env func(string) string) (Status, []Source) {
	bases := append(roots(env, "KIMI_CODE_HOME"), roots(env, "KIMI_SHARE_DIR")...)
	if len(bases) == 0 {
		bases = []string{filepath.Join(home, ".kimi-code"), filepath.Join(home, ".kimi")}
	}
	var dirs []string
	for _, b := range bases {
		dirs = append(dirs, filepath.Join(b, "sessions"))
	}
	return discoverFiles(k.ID(), dirs, func(p string, _ fs.DirEntry) bool {
		base := filepath.Base(p)
		if base == "wire.jsonl" {
			return filepath.Base(filepath.Dir(filepath.Dir(p))) == "agents"
		}
		return kimiContextRe.MatchString(base)
	})
}

var (
	kimiContextRe   = regexp.MustCompile(`^context(_\d+)?\.jsonl$`)
	kimiFailRe      = regexp.MustCompile(`^Command failed with exit code: (-?\d+)\.$`)
	kimiLegacyFail  = regexp.MustCompile(`^<system>ERROR: Command failed with exit code: (-?\d+)\.</system>$`)
	kimiLegacyOK    = "<system>Command executed successfully.</system>"
	kimiOK          = "Command executed successfully."
	kimiSystemTagRe = regexp.MustCompile(`^<system>.*</system>$`)
)

var kimiTools = map[string]model.Kind{
	"bash":           model.KindShell,
	"shell":          model.KindShell,
	"write":          model.KindEdit,
	"edit":           model.KindEdit,
	"writefile":      model.KindEdit,
	"strreplacefile": model.KindEdit,
}

func (k Kimi) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	if filepath.Base(src.Path) == "wire.jsonl" {
		return k.parseWire(src, repoRoot, since)
	}
	return k.parseLegacy(src, repoRoot, since)
}

func (k Kimi) parseWire(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	agentDir := filepath.Dir(src.Path)
	sessionDir := filepath.Dir(filepath.Dir(agentDir))
	session := filepath.Base(sessionDir)
	if a := filepath.Base(agentDir); a != "main" {
		session += ":" + a
	}
	b := newBook()
	recognized := false
	cwd := ""
	call := func(ts time.Time, id, name string, a obj) {
		ev := model.Event{Agent: k.ID(), Session: session, TS: ts, Cwd: cwd}
		switch toolKind(name, kimiTools) {
		case model.KindShell:
			if bg, _ := boolOf(a, "run_in_background"); bg {
				return
			}
			ev.Kind = model.KindShell
			ev.Command = strings.TrimSpace(str(a, "command"))
			ev.Cwd = geminiCwd(cwd, str(a, "cwd"))
			b.add(id, ev)
		case model.KindEdit:
			ev.Kind = model.KindEdit
			ev.Path = str(a, "path")
			if ev.Path == "" {
				ev.Path = str(a, "file_path")
			}
			if ev.Path != "" {
				b.add(id, ev)
			}
		}
	}
	result := func(id string, output any, isErr bool) {
		e := b.get(id)
		if e == nil {
			return
		}
		if e.ev.Kind == model.KindEdit {
			if isErr {
				e.drop = true
			}
			return
		}
		kimiShellResult(&e.ev, text(output))
	}
	_, _, err := scanJSONL(src.Path, func(m obj) {
		ts := parseTime(m["time"])
		switch str(m, "type") {
		case "metadata":
			if str(m, "protocol_version") != "" {
				recognized = true
			}
		case "config.update", "profile.bind":
			if v := str(m, "cwd"); v != "" {
				cwd = v
			} else if v := str(m, "environmentDisclosure", "cwd"); v != "" {
				cwd = v
			}
		case "context.append_loop_event":
			recognized = true
			ev := mapOf(m, "event")
			switch str(ev, "type") {
			case "tool.call":
				call(ts, str(ev, "toolCallId"), str(ev, "name"), args(ev["args"]))
			case "tool.result":
				isErr, _ := boolOf(ev, "result", "isError")
				result(str(ev, "toolCallId"), get(ev, "result", "output"), isErr)
			}
		case "context.append_message":
			recognized = true
			msg := mapOf(m, "message")
			switch str(msg, "role") {
			case "assistant":
				for _, tc := range list(msg, "toolCalls") {
					call(ts, str(tc, "id"), str(tc, "name"), args(get(tc, "arguments")))
				}
			case "tool":
				isErr, _ := boolOf(msg, "isError")
				result(str(msg, "toolCallId"), msg["content"], isErr)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no Kimi Code wire records")
	}
	events := b.events()
	if cwd == "" {
		if wd := kimiIndexWorkDir(sessionDir); wd != "" {
			for i := range events {
				if events[i].Cwd == "" {
					events[i].Cwd = wd
				}
			}
		}
	}
	return filterRepo(events, repoRoot, since), nil
}

// kimiShellResult reads the Bash tool's own trailer line.
func kimiShellResult(ev *model.Event, out string) {
	s := strings.TrimRight(out, " \t\r\n")
	if strings.TrimSpace(s) == kimiOK {
		ev.Exit = model.IntPtr(0)
		return
	}
	last := s
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		last = s[i+1:]
	}
	if code := matchInt(kimiFailRe, strings.TrimSpace(last)); code != nil {
		ev.Exit = code
		body := strings.TrimSuffix(s, last)
		ev.StderrHead = norm.Head(body)
		return
	}
	ev.StdoutHead = norm.Head(s)
}

// kimiIndexWorkDir looks the session up in <home>/session_index.jsonl.
func kimiIndexWorkDir(sessionDir string) string {
	home := filepath.Dir(filepath.Dir(filepath.Dir(sessionDir))) // sessions/<key>/<id>
	id := filepath.Base(sessionDir)
	wd := ""
	scanJSONL(filepath.Join(home, "session_index.jsonl"), func(m obj) {
		if str(m, "sessionDir") == sessionDir || (str(m, "sessionDir") == "" && str(m, "sessionId") == id) {
			if v := str(m, "workDir"); v != "" {
				wd = v
			}
		}
	})
	return wd
}

func (k Kimi) parseLegacy(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	sessionDir := filepath.Dir(src.Path)
	session := filepath.Base(sessionDir)
	bucket := filepath.Base(filepath.Dir(sessionDir))
	cwd := kimiLegacyWorkDir(filepath.Dir(filepath.Dir(filepath.Dir(sessionDir))), bucket)
	b := newBook()
	recognized := false
	_, _, err := scanJSONL(src.Path, func(m obj) {
		role := str(m, "role")
		switch role {
		case "_checkpoint", "_usage", "_system_prompt":
			recognized = true
			return
		case "user", "system":
			recognized = true
			return
		case "assistant":
			recognized = true
			for _, tc := range list(m, "tool_calls") {
				name := str(tc, "function", "name")
				a := args(get(tc, "function", "arguments"))
				ev := model.Event{Agent: k.ID(), Session: session, Cwd: cwd}
				switch toolKind(name, kimiTools) {
				case model.KindShell:
					ev.Kind = model.KindShell
					ev.Command = strings.TrimSpace(str(a, "command"))
					b.add(str(tc, "id"), ev)
				case model.KindEdit:
					ev.Kind = model.KindEdit
					ev.Path = str(a, "path")
					if ev.Path != "" {
						b.add(str(tc, "id"), ev)
					}
				}
			}
		case "tool":
			recognized = true
			e := b.get(str(m, "tool_call_id"))
			if e == nil {
				return
			}
			var parts []string
			switch c := m["content"].(type) {
			case string:
				parts = []string{c}
			case []any:
				for _, p := range c {
					if s := str(p, "text"); s != "" {
						parts = append(parts, s)
					}
				}
			}
			if len(parts) == 0 {
				return
			}
			first := strings.TrimSpace(parts[0])
			if e.ev.Kind == model.KindEdit {
				if strings.HasPrefix(first, "<system>ERROR") {
					e.drop = true
				}
				return
			}
			rest := strings.Join(parts[1:], "\n")
			switch {
			case first == kimiLegacyOK:
				e.ev.Exit = model.IntPtr(0)
				e.ev.StdoutHead = norm.Head(rest)
			case kimiLegacyFail.MatchString(first):
				e.ev.Exit = matchInt(kimiLegacyFail, first)
				e.ev.StderrHead = norm.Head(rest)
			case !kimiSystemTagRe.MatchString(first):
				e.ev.StdoutHead = norm.Head(strings.Join(parts, "\n"))
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no Kimi CLI context messages")
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

// kimiLegacyWorkDir maps a legacy session bucket (md5 of the work dir path)
// back to the path recorded in <share>/kimi.json.
func kimiLegacyWorkDir(share, bucket string) string {
	var meta obj
	if readJSON(filepath.Join(share, "kimi.json"), &meta) != nil {
		return ""
	}
	for _, wd := range list(meta, "work_dirs") {
		p := str(wd, "path")
		if p == "" {
			continue
		}
		if kaos := str(wd, "kaos"); kaos != "" && kaos != "local" {
			continue
		}
		sum := md5.Sum([]byte(p))
		if hex.EncodeToString(sum[:]) == bucket {
			return p
		}
	}
	return ""
}
