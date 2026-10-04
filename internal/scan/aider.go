package scan

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Aider reads Aider's markdown chat log: ~/.aider.chat.history.md (Discover)
// and the repo-local <repoRoot>/.aider.chat.history.md (DiscoverRepo). There
// is no env override.
//
// Format (aider/io.py): each run starts "# aider chat started at
// YYYY-MM-DD HH:MM:SS" (local time); user input is "#### <line>"; tool output
// is blockquoted "> <line>". Commands appear as "#### /run cmd",
// "#### /test cmd", "#### !cmd", and for shell commands the model suggested,
// "> Running cmd" (coders/base_coder.py). Edits appear as
// "> Applied edit to <path>" with a path relative to the repo root.
//
// Exit status: none. run_cmd returns the exit status, but aider never writes
// it (or any "exit code"/"exit status" marker) to the history file, and it
// never marks a command as passed; /test only implies a non-zero exit by
// adding output, without the code. The spec accepts an aider command only
// when the log proves its exit code, so every shell event has Exit nil and
// is dropped downstream. Model prose ("tests pass now") is never read.
//
// Cwd: the repo-local log was written by aider running at that repo's root
// (/run and suggested commands use cwd=coder.root), so its events get
// cwd = repoRoot. The home log has no cwd and relative edit paths, so it
// matches no repo.
type Aider struct{}

func (Aider) ID() string { return "aider" }

const aiderHistory = ".aider.chat.history.md"

func (a Aider) Discover(home string, env func(string) string) (Status, []Source) {
	return discoverFiles(a.ID(), []string{filepath.Join(home, aiderHistory)}, hasExt(".md"))
}

func (a Aider) DiscoverRepo(repoRoot string) []Source {
	p := filepath.Join(repoRoot, aiderHistory)
	if info, err := os.Stat(p); err != nil || info.IsDir() {
		return nil
	}
	return []Source{{Agent: a.ID(), Path: p, Kind: "file"}}
}

const aiderStart = "# aider chat started at "

func (a Aider) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	f, err := os.Open(src.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cwd := ""
	if repoRoot != "" && filepath.Clean(src.Path) == filepath.Join(repoRoot, aiderHistory) {
		cwd = repoRoot
	}
	var events []model.Event
	recognized := false
	var base model.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), maxLine)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if strings.HasPrefix(line, aiderStart) {
			recognized = true
			stamp := strings.TrimSpace(strings.TrimPrefix(line, aiderStart))
			ts, _ := time.ParseInLocation("2006-01-02 15:04:05", stamp, time.Local)
			base = model.Event{Agent: a.ID(), Session: src.Path + "#" + stamp, TS: ts.UTC(), Cwd: cwd}
			if ts.IsZero() {
				base.TS = time.Time{}
			}
			continue
		}
		if !recognized {
			continue
		}
		if cmd := aiderCommand(line); cmd != "" {
			ev := base
			ev.Kind = model.KindShell
			ev.Command = cmd // Exit stays nil: aider never logs it.
			events = append(events, ev)
			continue
		}
		if p, ok := strings.CutPrefix(line, "> Applied edit to "); ok && strings.TrimSpace(p) != "" {
			ev := base
			ev.Kind = model.KindEdit
			ev.Path = strings.TrimSpace(p)
			events = append(events, ev)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !recognized {
		return nil, unrecognized("no aider chat header")
	}
	return filterRepo(events, repoRoot, since), nil
}

// aiderCommand returns the shell command a history line records, if any.
func aiderCommand(line string) string {
	if rest, ok := strings.CutPrefix(line, "#### "); ok {
		rest = strings.TrimSpace(rest)
		for _, p := range []string{"/run ", "/test ", "!"} {
			if c, ok := strings.CutPrefix(rest, p); ok {
				return strings.TrimSpace(c)
			}
		}
		return ""
	}
	if c, ok := strings.CutPrefix(line, "> Running "); ok {
		return strings.TrimSpace(c)
	}
	return ""
}
