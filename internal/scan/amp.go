package scan

import (
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Amp reads Sourcegraph Amp's local thread mirror: one JSON document per
// thread at $XDG_DATA_HOME/amp/threads/T-<id>.json (default
// ~/.local/share/amp/threads). AMP_DATA_DIR (comma-separated) replaces the
// amp data dir; threads are under <dir>/threads.
//
// Partly unverified: Amp is closed source. The shape below comes from
// third-party readers of these files (codeburn's Amp provider,
// getagentseal/codeburn#1578; deja-vu issue vshulcz/deja-vu#4356), not from
// Amp itself, so the parser is strict and fails closed.
//
// A thread is {id, created?, env:{initial:{trees:[{uri}]}}, messages:[{role,
// content:[...], meta:{sentAt}, usage:{timestamp}}]}. Assistant content holds
// {type:"tool_use", id, name, input} blocks — Bash {cmd, cwd}, edit_file /
// create_file {path} — and a later user message holds {type:"tool_result",
// toolUseID, run:{status, result:{output, exitCode}}}.
//
// Exit status comes only from run.result.exitCode, and only when run.status
// is "done" or "error". Nothing else (status "done" without exitCode,
// cancelled or rejected runs) yields an exit. The cwd is input.cwd, else the
// first workspace tree file:// URI. Edits whose run.status is "error",
// "cancelled" or "rejected-by-user" are dropped.
type Amp struct{}

func (Amp) ID() string { return "amp" }

func (a Amp) Discover(home string, env func(string) string) (Status, []Source) {
	var dirs []string
	for _, d := range roots(env, "AMP_DATA_DIR", filepath.Join(xdgData(home, env), "amp")) {
		dirs = append(dirs, filepath.Join(d, "threads"))
	}
	return discoverFiles(a.ID(), dirs, hasExt(".json"))
}

var ampTools = map[string]model.Kind{
	"bash":        model.KindShell,
	"edit_file":   model.KindEdit,
	"create_file": model.KindEdit,
	"undo_edit":   "",
	"format_file": "",
}

func (a Amp) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	var th obj
	if err := readJSON(src.Path, &th); err != nil {
		return nil, unrecognized("amp thread: %v", err)
	}
	session := str(th, "id")
	msgs, ok := get(th, "messages").([]any)
	if session == "" || !ok {
		return nil, unrecognized("no Amp thread id/messages")
	}
	cwd := ""
	for _, t := range list(th, "env", "initial", "trees") {
		if p := ampURIPath(str(t, "uri")); p != "" {
			cwd = p
			break
		}
	}
	created := parseTime(th["created"])
	b := newBook()
	for _, m := range msgs {
		ts := parseTime(get(m, "meta", "sentAt"))
		if ts.IsZero() {
			ts = parseTime(get(m, "usage", "timestamp"))
		}
		if ts.IsZero() {
			ts = created
		}
		base := model.Event{Agent: a.ID(), Session: session, TS: ts, Cwd: cwd}
		for _, blk := range list(m, "content") {
			switch str(blk, "type") {
			case "tool_use":
				in := mapOf(blk, "input")
				switch toolKind(str(blk, "name"), ampTools) {
				case model.KindShell:
					ev := base
					ev.Kind = model.KindShell
					ev.Command = strings.TrimSpace(str(in, "cmd"))
					if ev.Command == "" {
						continue
					}
					if c := str(in, "cwd"); filepath.IsAbs(c) {
						ev.Cwd = c
					}
					b.add(str(blk, "id"), ev)
				case model.KindEdit:
					ev := base
					ev.Kind = model.KindEdit
					ev.Path = str(in, "path")
					if ev.Path == "" {
						continue
					}
					b.add(str(blk, "id"), ev)
				}
			case "tool_result":
				e := b.get(str(blk, "toolUseID"))
				if e == nil {
					continue
				}
				run := mapOf(blk, "run")
				status := str(run, "status")
				if e.ev.Kind == model.KindEdit {
					if status == "error" || status == "cancelled" || status == "rejected-by-user" {
						e.drop = true
					}
					continue
				}
				res := mapOf(run, "result")
				e.ev.StdoutHead = norm.Head(str(res, "output"))
				if status == "done" || status == "error" {
					e.ev.Exit = intOf(res, "exitCode")
				}
			}
		}
	}
	return filterRepo(b.events(), repoRoot, since), nil
}

// ampURIPath turns a file:// workspace URI into a path.
func ampURIPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return ""
	}
	return filepath.FromSlash(u.Path)
}
