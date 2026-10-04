package scan

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

// Antigravity reads Google Antigravity agent brain transcripts:
// <app>/brain/<conversationId>/.system_generated/logs/transcript.jsonl
// (transcript_full.jsonl, the untruncated copy, is preferred when both
// exist), where <app> is each $ANTIGRAVITY_DATA_DIR entry, else every
// ~/.gemini/antigravity* directory (the IDE and antigravity-cli).
//
// UNVERIFIED: Antigravity is closed source. The record shape is taken from
// public third-party readers (kenn-io/agentsview
// internal/parser/antigravity_brain_transcript.go, issue examples): one step
// per line {step_index, source, type, status, created_at, content, thinking,
// tool_calls:[{name, args}]}. A PLANNER_RESPONSE step carries the calls;
// run_command args are {CommandLine, Cwd}; write_to_file /
// replace_file_content / multi_replace_file_content args carry TargetFile.
// The command's output arrives in a later RUN_COMMAND step, matched in order
// because calls carry no id.
//
// No structured exit field is known, and the only reported exit text ("The
// command exited with code N") has no verifiable source, so every shell
// event has Exit nil and is dropped downstream. Edits cannot be checked for
// failure and are kept as recorded.
type Antigravity struct{}

func (Antigravity) ID() string { return "antigravity" }

func (a Antigravity) Discover(home string, env func(string) string) (Status, []Source) {
	bases := roots(env, "ANTIGRAVITY_DATA_DIR")
	if len(bases) == 0 {
		bases = globFiles(filepath.Join(home, ".gemini", "antigravity*"))
		if len(bases) == 0 {
			bases = []string{filepath.Join(home, ".gemini", "antigravity")}
		}
	}
	var dirs []string
	for _, b := range bases {
		dirs = append(dirs, filepath.Join(b, "brain"))
	}
	st, srcs := discoverFiles(a.ID(), dirs, func(p string, _ fs.DirEntry) bool {
		base := filepath.Base(p)
		if base != "transcript.jsonl" && base != "transcript_full.jsonl" {
			return false
		}
		logs := filepath.Dir(p)
		return filepath.Base(logs) == "logs" && filepath.Base(filepath.Dir(logs)) == ".system_generated"
	})
	// One conversation, one source: drop transcript.jsonl beside a full copy.
	full := map[string]bool{}
	for _, s := range srcs {
		if filepath.Base(s.Path) == "transcript_full.jsonl" {
			full[filepath.Dir(s.Path)] = true
		}
	}
	var out []Source
	for _, s := range srcs {
		if filepath.Base(s.Path) == "transcript.jsonl" && full[filepath.Dir(s.Path)] {
			continue
		}
		out = append(out, s)
	}
	return st, out
}

var antigravityTools = map[string]model.Kind{
	"run_command":                model.KindShell,
	"write_to_file":              model.KindEdit,
	"replace_file_content":       model.KindEdit,
	"multi_replace_file_content": model.KindEdit,
}

type antigravityStep struct {
	index int64
	m     obj
}

func (a Antigravity) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	// brain/<id>/.system_generated/logs/<file>
	session := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(src.Path))))
	var steps []antigravityStep
	_, _, err := scanJSONL(src.Path, func(m obj) {
		idx := intOf(m, "step_index")
		if idx == nil || (str(m, "type") == "" && str(m, "source") == "") {
			return
		}
		steps = append(steps, antigravityStep{int64(*idx), m})
	})
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, unrecognized("no Antigravity transcript steps")
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].index < steps[j].index })

	b := newBook()
	var pending []*entry // run_command calls waiting for their RUN_COMMAND step
	for _, s := range steps {
		m := s.m
		ts := parseTime(m["created_at"])
		if str(m, "type") == "RUN_COMMAND" {
			if len(pending) > 0 {
				pending[0].ev.StdoutHead = norm.Head(str(m, "content"))
				pending = pending[1:]
			}
			continue
		}
		for _, tc := range list(m, "tool_calls") {
			args := args(get(tc, "args"))
			ev := model.Event{Agent: a.ID(), Session: session, TS: ts}
			switch toolKind(str(tc, "name"), antigravityTools) {
			case model.KindShell:
				ev.Kind = model.KindShell
				ev.Command = strings.TrimSpace(str(args, "CommandLine"))
				ev.Cwd = str(args, "Cwd")
				if ev.Command == "" {
					continue
				}
				pending = append(pending, b.add("", ev))
			case model.KindEdit:
				ev.Kind = model.KindEdit
				ev.Path = str(args, "TargetFile")
				if ev.Path == "" {
					continue
				}
				b.add("", ev)
			}
		}
	}
	return filterRepo(b.events(), repoRoot, since), nil
}
