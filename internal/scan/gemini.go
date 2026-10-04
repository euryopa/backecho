package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Gemini reads Gemini CLI chat recordings:
// <root>/tmp/<project>/chats/session-*.json (legacy) and *.jsonl (current),
// where <root> is $GEMINI_DATA_DIR, else $GEMINI_CLI_HOME/.gemini (gemini-cli
// treats GEMINI_CLI_HOME as the home that holds .gemini), else ~/.gemini
// (and ~/.cache/.gemini, used under the macOS seatbelt sandbox).
//
// Verified against google-gemini/gemini-cli packages/core/src/services/
// chatRecordingService.ts and chatRecordingTypes.ts. A .json file is one
// ConversationRecord {sessionId, projectHash, startTime, lastUpdated,
// messages}. A .jsonl file starts with that metadata and then appends message
// records (a message is re-appended whole each time its toolCalls change, so
// the last copy of an id wins), "$set" metadata updates (whose messages, when
// present, are upserted too), "$rewindTo" and "$patch" records. Rewinds and
// patches only reshape what the model sees later; the commands still ran, so
// they are ignored. Messages of type "gemini" carry toolCalls [{id, name,
// args, result, status, timestamp}] where result is the functionResponse
// parts sent back to the model ({functionResponse:{response:{output|error}}}).
//
// The file has no cwd. projectHash is sha256 of the project root
// (paths.ts getProjectHash): when it equals sha256(repoRoot) the session cwd
// is repoRoot. run_shell_command's dir_path (older: directory) is resolved
// against it; an absolute one is used as is. Otherwise the session is
// matched by absolute write_file/replace file_path.
//
// Exit status comes only from the "Exit Code: N" line run_shell_command
// formats into its llmContent (tools/shell.ts). Older versions write it on
// every completed command ("Exit Code: 0" included, "(none)" when killed by a
// signal); current versions write it only when the code is non-zero, so a
// current-version success has no exit and is dropped downstream. Cancelled,
// backgrounded and summarized results carry no such line and stay unknown.
// Edits whose call status is "error"/"cancelled" or whose response is an
// error are dropped.
type Gemini struct{}

func (Gemini) ID() string { return "gemini" }

func (g Gemini) Discover(home string, env func(string) string) (Status, []Source) {
	bases := roots(env, "GEMINI_DATA_DIR")
	if len(bases) == 0 {
		for _, h := range roots(env, "GEMINI_CLI_HOME") {
			bases = append(bases, filepath.Join(h, ".gemini"))
		}
	}
	if len(bases) == 0 {
		bases = []string{filepath.Join(home, ".gemini"), filepath.Join(home, ".cache", ".gemini")}
	}
	var dirs []string
	for _, b := range bases {
		dirs = append(dirs, filepath.Join(b, "tmp"))
	}
	return discoverFiles(g.ID(), dirs, geminiChatFile)
}

// geminiChatFile matches <...>/chats/<name>.json or .jsonl.
func geminiChatFile(p string, _ fs.DirEntry) bool {
	if filepath.Base(filepath.Dir(p)) != "chats" {
		return false
	}
	if strings.HasSuffix(p, ".runtime.json") {
		return false
	}
	return strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".jsonl")
}

func (g Gemini) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return geminiParseFile(g.ID(), src.Path, repoRoot, since)
}

var geminiTools = map[string]model.Kind{
	"run_shell_command": model.KindShell,
	"write_file":        model.KindEdit,
	"replace":           model.KindEdit,
	"edit":              model.KindEdit,
}

var (
	geminiExitRe   = regexp.MustCompile(`(?m)^Exit Code: (-?\d+)\s*$`)
	geminiOutputRe = regexp.MustCompile(`(?m)^Output: (.*)$`)
	geminiErrorRe  = regexp.MustCompile(`(?m)^Error: (.*)$`)
)

// geminiConversation is a replayed chat file.
type geminiConversation struct {
	sessionID, projectHash string
	order                  []string
	msgs                   map[string]obj
}

func (c *geminiConversation) upsert(m obj) {
	id := str(m, "id")
	if id == "" {
		return
	}
	if _, ok := c.msgs[id]; !ok {
		c.order = append(c.order, id)
	}
	c.msgs[id] = m
}

func (c *geminiConversation) meta(m obj) {
	if v := str(m, "sessionId"); v != "" {
		c.sessionID = v
	}
	if v := str(m, "projectHash"); v != "" {
		c.projectHash = v
	}
	for _, msg := range list(m, "messages") {
		if mm, ok := msg.(obj); ok {
			c.upsert(mm)
		}
	}
}

// geminiParseFile reads a Gemini-CLI-shaped chat file (also used for legacy
// Qwen Code chats) and emits events attributed to agent.
func geminiParseFile(agent, path, repoRoot string, since time.Time) ([]model.Event, error) {
	c := &geminiConversation{msgs: map[string]obj{}}
	if strings.HasSuffix(path, ".jsonl") {
		_, _, err := scanJSONL(path, func(m obj) {
			switch {
			case m["$rewindTo"] != nil || m["$patch"] != nil:
			case mapOf(m, "$set") != nil:
				c.meta(mapOf(m, "$set"))
			case str(m, "sessionId") != "" && str(m, "projectHash") != "":
				c.meta(m)
			case str(m, "id") != "" && str(m, "type") != "":
				c.upsert(m)
			}
		})
		if err != nil {
			return nil, err
		}
	} else {
		var m obj
		if err := readJSON(path, &m); err != nil {
			return nil, unrecognized("not a JSON chat record: %v", err)
		}
		if _, ok := m["messages"].([]any); !ok {
			return nil, unrecognized("no messages array")
		}
		c.meta(m)
	}
	if c.sessionID == "" || c.projectHash == "" {
		return nil, unrecognized("no Gemini chat metadata (sessionId, projectHash)")
	}

	var base string
	if geminiHash(repoRoot) == strings.ToLower(c.projectHash) {
		base = repoRoot
	}
	b := newBook()
	for _, id := range c.order {
		msg := c.msgs[id]
		if str(msg, "type") != "gemini" {
			continue
		}
		msgTS := parseTime(msg["timestamp"])
		for _, tc := range list(msg, "toolCalls") {
			name := str(tc, "name")
			a := args(get(tc, "args"))
			ev := model.Event{Agent: agent, Session: c.sessionID, TS: parseTime(get(tc, "timestamp"))}
			if ev.TS.IsZero() {
				ev.TS = msgTS
			}
			status := str(tc, "status")
			resText, resErr := geminiResult(get(tc, "result"))
			switch toolKind(name, geminiTools) {
			case model.KindShell:
				ev.Kind = model.KindShell
				ev.Command = strings.TrimSpace(str(a, "command"))
				ev.Cwd = geminiCwd(base, str(a, "dir_path"), str(a, "directory"))
				if bg, _ := boolOf(a, "is_background"); bg {
					continue
				}
				if status == "cancelled" {
					b.add(str(tc, "id"), ev)
					continue
				}
				geminiShellResult(&ev, resText+"\n"+resErr)
				b.add(str(tc, "id"), ev)
			case model.KindEdit:
				ev.Kind = model.KindEdit
				ev.Path = str(a, "file_path")
				if ev.Path == "" {
					ev.Path = str(a, "absolute_path")
				}
				if ev.Path == "" || status == "error" || status == "cancelled" || resErr != "" {
					continue
				}
				// Edit paths are as recorded; a relative one resolves against
				// the session cwd when known.
				ev.Cwd = base
				b.add(str(tc, "id"), ev)
			}
		}
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

func geminiHash(p string) string {
	if p == "" {
		return ""
	}
	h := sha256.Sum256([]byte(p))
	return hex.EncodeToString(h[:])
}

// geminiCwd resolves the shell tool's directory argument.
func geminiCwd(base string, dirs ...string) string {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if filepath.IsAbs(d) {
			return d
		}
		if base == "" {
			return ""
		}
		return filepath.Join(base, d)
	}
	return base
}

// geminiResult flattens a recorded tool result (PartListUnion) into its
// output text and its error text.
func geminiResult(v any) (out, errText string) {
	var outs, errs []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			outs = append(outs, x)
		case []any:
			for _, p := range x {
				walk(p)
			}
		case obj:
			if fr := mapOf(x, "functionResponse"); fr != nil {
				r := mapOf(fr, "response")
				if s := str(r, "output"); s != "" {
					outs = append(outs, s)
				}
				if s := str(r, "error"); s != "" {
					errs = append(errs, s)
				}
				return
			}
			if s := str(x, "text"); s != "" {
				outs = append(outs, s)
			}
		}
	}
	walk(v)
	return strings.Join(outs, "\n"), strings.Join(errs, "\n")
}

// geminiShellResult reads the run_shell_command llmContent. The tool appends
// its trailer lines after the command output, so the last "Exit Code:" line
// is the tool's own.
func geminiShellResult(ev *model.Event, s string) {
	s = strings.ReplaceAll(s, "<untrusted_context>", "")
	s = strings.ReplaceAll(s, "</untrusted_context>", "")
	if all := geminiExitRe.FindAllStringSubmatch(s, -1); len(all) > 0 {
		ev.Exit = matchInt(geminiExitRe, all[len(all)-1][0])
	}
	if m := geminiOutputRe.FindStringSubmatch(s); m != nil && m[1] != "(empty)" {
		ev.StdoutHead = norm.Head(m[1])
	}
	if m := geminiErrorRe.FindStringSubmatch(s); m != nil && m[1] != "(none)" {
		ev.StderrHead = norm.Head(m[1])
	}
}
