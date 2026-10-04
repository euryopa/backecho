package scan

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// maxLine is the largest JSONL line read. Tool results can be large; a line
// over this is skipped by stopping the file, never by guessing.
const maxLine = 64 << 20

type obj = map[string]any

// scanJSONL calls fn for every line that decodes to a JSON object. A line
// that fails json.Unmarshal is skipped and counted.
func scanJSONL(path string, fn func(m obj)) (good, bad int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			if len(strings.TrimSpace(string(line))) > 0 {
				bad++
			}
			continue
		}
		var m obj
		if json.Unmarshal(line, &m) != nil {
			bad++
			continue
		}
		good++
		fn(m)
	}
	return good, bad, sc.Err()
}

// readJSON decodes a whole JSON file.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// get walks nested objects by key.
func get(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(obj)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func str(v any, keys ...string) string {
	s, _ := get(v, keys...).(string)
	return s
}

func mapOf(v any, keys ...string) obj {
	m, _ := get(v, keys...).(obj)
	return m
}

func list(v any, keys ...string) []any {
	l, _ := get(v, keys...).([]any)
	return l
}

func boolOf(v any, keys ...string) (val, ok bool) {
	val, ok = get(v, keys...).(bool)
	return
}

// intOf returns a JSON number (or numeric string) as an int, nil if absent.
func intOf(v any, keys ...string) *int {
	switch n := get(v, keys...).(type) {
	case float64:
		if n != float64(int(n)) {
			return nil
		}
		return model.IntPtr(int(n))
	case json.Number:
		i, err := strconv.Atoi(string(n))
		if err == nil {
			return model.IntPtr(i)
		}
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err == nil {
			return model.IntPtr(i)
		}
	}
	return nil
}

// firstInt returns the first present integer field among keys of m.
func firstInt(m any, keys ...string) *int {
	for _, k := range keys {
		if i := intOf(m, k); i != nil {
			return i
		}
	}
	return nil
}

// args decodes tool arguments that may be an object or a JSON string.
func args(v any) obj {
	switch a := v.(type) {
	case obj:
		return a
	case string:
		var m obj
		if json.Unmarshal([]byte(a), &m) == nil {
			return m
		}
	}
	return nil
}

// parseTime accepts RFC 3339 strings and unix seconds or milliseconds.
func parseTime(v any) time.Time {
	switch t := v.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
			if ts, err := time.Parse(layout, t); err == nil {
				return ts.UTC()
			}
		}
		if n, err := strconv.ParseFloat(t, 64); err == nil {
			return parseTime(n)
		}
	case float64:
		return unixAny(int64(t))
	case int64:
		return unixAny(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return unixAny(n)
		}
	}
	return time.Time{}
}

func unixAny(n int64) time.Time {
	switch {
	case n <= 0:
		return time.Time{}
	case n > 1e17: // nanoseconds
		return time.Unix(0, n).UTC()
	case n > 1e14: // microseconds
		return time.UnixMicro(n).UTC()
	case n > 1e11: // milliseconds
		return time.UnixMilli(n).UTC()
	}
	return time.Unix(n, 0).UTC()
}

// text flattens a tool result content: a string, or a list of
// {type:"text", text} blocks.
func text(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			if s := str(b, "text"); s != "" {
				parts = append(parts, s)
			} else if s, ok := b.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	case obj:
		if s := str(c, "text"); s != "" {
			return s
		}
		if s := str(c, "output"); s != "" {
			return s
		}
	}
	return ""
}

var (
	shellTools = map[string]bool{"bash": true, "shell": true, "exec": true, "run_terminal_cmd": true, "local_shell": true, "execute_command": true, "powershell": true}
	editTools  = map[string]bool{"edit": true, "write": true, "multiedit": true, "apply_patch": true, "search_replace": true, "strreplace": true, "create_file": true}
)

// toolKind classifies a tool name, case-insensitive, after stripping a vendor
// prefix (mcp__server__, functions., developer__). extra holds names a
// vendor uses beyond the common lists.
func toolKind(name string, extra map[string]model.Kind) model.Kind {
	n := strings.ToLower(strings.TrimSpace(name))
	if k, ok := extra[n]; ok {
		return k
	}
	for _, sep := range []string{"__", ".", "/", ":"} {
		if i := strings.LastIndex(n, sep); i >= 0 {
			n = n[i+len(sep):]
		}
	}
	if k, ok := extra[n]; ok {
		return k
	}
	switch {
	case shellTools[n]:
		return model.KindShell
	case editTools[n]:
		return model.KindEdit
	}
	return ""
}

// shellCommand turns a command that may be a string or an argv into text.
// `bash -lc "..."` and friends unwrap to the script.
func shellCommand(v any) string {
	switch c := v.(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		var argv []string
		for _, a := range c {
			s, ok := a.(string)
			if !ok {
				return ""
			}
			argv = append(argv, s)
		}
		if len(argv) >= 3 {
			base := filepath.Base(argv[0])
			if (base == "bash" || base == "sh" || base == "zsh" || base == "pwsh" || base == "powershell") && (argv[1] == "-lc" || argv[1] == "-c" || argv[1] == "-Command") {
				return strings.TrimSpace(strings.Join(argv[2:], " "))
			}
		}
		return strings.TrimSpace(strings.Join(argv, " "))
	}
	return ""
}

// patchPaths lists the files an apply_patch body touches.
func patchPaths(patch string) []string {
	var out []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, p := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if strings.HasPrefix(line, p) {
				out = append(out, strings.TrimSpace(strings.TrimPrefix(line, p)))
			}
		}
		if strings.HasPrefix(line, "+++ b/") {
			out = append(out, strings.TrimPrefix(line, "+++ b/"))
		}
	}
	return out
}

// firstLineOf reads only the first non-empty line of a spilled output file.
func firstLineOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			return norm.Head(l)
		}
	}
	return ""
}

// book keeps tool calls in log order and pairs each with its result by
// vendor id.
type book struct {
	order []*entry
	byID  map[string]*entry
}

type entry struct {
	ev   model.Event
	drop bool
}

func newBook() *book { return &book{byID: map[string]*entry{}} }

func (b *book) add(id string, ev model.Event) *entry {
	e := &entry{ev: ev}
	b.order = append(b.order, e)
	if id != "" {
		b.byID[id] = e
	}
	return e
}

func (b *book) get(id string) *entry {
	if id == "" {
		return nil
	}
	return b.byID[id]
}

func (b *book) events() []model.Event {
	var out []model.Event
	for _, e := range b.order {
		if !e.drop {
			out = append(out, e.ev)
		}
	}
	return out
}

// filterRepo keeps events from sessions that ran in the repo. A session
// matches when a recorded cwd is the toplevel or below it; a session with no
// cwd at all matches when an absolute edit path is inside the toplevel.
// Events whose own cwd is outside the repo are dropped.
func filterRepo(events []model.Event, root string, since time.Time) []model.Event {
	in := map[string]bool{}
	hasCwd := map[string]bool{}
	for _, e := range events {
		if e.Cwd == "" {
			continue
		}
		hasCwd[e.Session] = true
		if _, ok := norm.Rel(root, e.Cwd); ok {
			in[e.Session] = true
		}
	}
	for _, e := range events {
		if hasCwd[e.Session] || e.Kind != model.KindEdit || !filepath.IsAbs(e.Path) {
			continue
		}
		if _, ok := norm.Rel(root, e.Path); ok {
			in[e.Session] = true
		}
	}
	var out []model.Event
	for _, e := range events {
		if !in[e.Session] {
			continue
		}
		if e.Cwd != "" {
			if _, ok := norm.Rel(root, e.Cwd); !ok {
				continue
			}
		}
		if !since.IsZero() && !e.TS.IsZero() && e.TS.Before(since) {
			continue
		}
		out = append(out, e)
	}
	return out
}

var exitLineRe = regexp.MustCompile(`(?m)^\s*(?:Error:\s*)?Exit code:?\s+(-?\d+)\s*$`)

// matchInt returns the first capture of re in s as an int.
func matchInt(re *regexp.Regexp, s string) *int {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	i, err := strconv.Atoi(m[1])
	if err != nil {
		return nil
	}
	return &i
}

func sessionFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
