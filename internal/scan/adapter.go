// Package scan maps each agent's native session log onto model.Event. Every
// vendor lives in its own file with its own fixtures and fails closed: a
// record it does not recognize yields nothing.
package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

type Source struct {
	Agent string
	Path  string
	Kind  string // "file" or "sqlite"
}

type Status struct {
	Agent  string
	State  string // "found", "empty", "skipped"
	Detail string
	Events int
}

type Adapter interface {
	ID() string
	Discover(home string, env func(string) string) (Status, []Source)
	Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error)
}

// RepoAdapter is implemented by tools that also write a transcript inside the
// repo (.crush/crush.db, .aider.chat.history.md). Only those known paths are
// looked at; the repo is never walked for logs.
type RepoAdapter interface {
	DiscoverRepo(repoRoot string) []Source
}

// ErrUnrecognized marks a file whose schema the parser does not know. The
// file is skipped and counted, the process does not fail.
var ErrUnrecognized = errors.New("unrecognized schema")

func unrecognized(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnrecognized, fmt.Sprintf(format, args...))
}

// All returns every adapter, in the order `backecho status` lists them.
func All() []Adapter {
	return []Adapter{
		Claude{},
		Codex{},
		Cursor{},
		Copilot{},
		OpenCode{},
		Gemini{},
		Amp{},
		Goose{},
		Hermes{},
		Pi{},
		Cline{},
		Aider{},
		Kimi{},
		Qwen{},
		Grok{},
		Antigravity{},
		Continue{},
		Droid{},
		OpenClaw{},
		Crush{},
		CursorIDE{},
	}
}

// IDs lists the registered adapter ids.
func IDs() []string {
	var out []string
	for _, a := range All() {
		out = append(out, a.ID())
	}
	return out
}

// roots splits an env override (comma-separated) or falls back to defaults.
func roots(env func(string) string, key string, defaults ...string) []string {
	if key != "" {
		if v := strings.TrimSpace(env(key)); v != "" {
			var out []string
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			return out
		}
	}
	return defaults
}

func xdgData(home string, env func(string) string) string {
	if v := env("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".local", "share")
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// walkFiles collects files under each root that match. Missing roots are
// skipped; unreadable directories are reported.
func walkFiles(rootDirs []string, match func(path string, d fs.DirEntry) bool) (files []string, anyRoot bool, errs []string) {
	seen := map[string]bool{}
	for _, r := range rootDirs {
		info, err := os.Stat(r)
		if err != nil {
			continue
		}
		anyRoot = true
		if !info.IsDir() {
			if !seen[r] {
				seen[r] = true
				files = append(files, r)
			}
			continue
		}
		filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				errs = append(errs, err.Error())
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || seen[p] {
				return nil
			}
			if match(p, d) {
				seen[p] = true
				files = append(files, p)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files, anyRoot, errs
}

// discoverFiles is the common Discover for file-based adapters.
func discoverFiles(agent string, rootDirs []string, match func(path string, d fs.DirEntry) bool) (Status, []Source) {
	files, anyRoot, errs := walkFiles(rootDirs, match)
	st := Status{Agent: agent}
	if !anyRoot {
		st.State = "empty"
		st.Detail = "no log root at " + strings.Join(rootDirs, ", ")
		return st, nil
	}
	var srcs []Source
	for _, f := range files {
		srcs = append(srcs, Source{Agent: agent, Path: f, Kind: "file"})
	}
	switch {
	case len(srcs) > 0:
		st.State = "found"
		st.Detail = fmt.Sprintf("%d files", len(srcs))
	case len(errs) > 0:
		st.State = "skipped"
		st.Detail = errs[0]
	default:
		st.State = "empty"
		st.Detail = "no session files under " + strings.Join(rootDirs, ", ")
	}
	return st, srcs
}

func hasExt(exts ...string) func(string, fs.DirEntry) bool {
	return func(p string, _ fs.DirEntry) bool {
		for _, e := range exts {
			if strings.HasSuffix(p, e) {
				return true
			}
		}
		return false
	}
}

// matchGlob returns files matching a filepath.Glob pattern list.
func globFiles(patterns ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range patterns {
		m, _ := filepath.Glob(p)
		for _, f := range m {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}
