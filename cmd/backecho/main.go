// Command backecho mines local AI coding agent session logs for commands that
// actually succeeded and writes them to .backecho/verify.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// version is set at release time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `backecho — keep the commands your coding agents proved work.

Usage:
  backecho sync [--agent a,b] [--dry-run] [--since YYYY-MM-DD] [--dir DIR]
  backecho show [--json] [--dir DIR]
  backecho status [--json] [--agent a,b]
  backecho stale [--json] [--dir DIR]
  backecho path [--dir DIR]
  backecho version

Commands:
  sync     scan every detected agent's logs for this repo and update .backecho/
  show     print verify.md and local.md as rendered from the store
  status   list every adapter: found, empty, or skipped, and why
  stale    list entries that have not passed in 30 days or fail more than pass
  path     print the data directory

The data directory is .backecho at the git toplevel. Override it with --dir
or BACKECHO_DIR. backecho makes no network calls and sends no telemetry.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, time.Now()))
}

// app carries what every subcommand needs. Tests build one directly.
type app struct {
	stdout, stderr io.Writer
	env            func(string) string
	now            time.Time
	cwd            string
	home           string
}

func run(args []string, stdout, stderr io.Writer, env func(string) string, now time.Time) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "backecho:", err)
		return exitError
	}
	a := &app{stdout: stdout, stderr: stderr, env: env, now: now.UTC(), cwd: cwd, home: homeDir(env)}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "sync":
		return a.sync(rest)
	case "show":
		return a.show(rest)
	case "status":
		return a.status(rest)
	case "stale":
		return a.stale(rest)
	case "path":
		return a.path(rest)
	case "version", "--version", "-version":
		fmt.Fprintln(stdout, "backecho", version)
		return exitOK
	case "help", "-h", "--help", "-help":
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	fmt.Fprintf(stderr, "backecho: unknown command %q\n\n%s", cmd, usage)
	return exitUsage
}

func homeDir(env func(string) string) string {
	for _, k := range []string{"HOME", "USERPROFILE"} {
		if v := env(k); v != "" {
			return v
		}
	}
	h, _ := os.UserHomeDir()
	return h
}

// flags builds a FlagSet that reports errors as usage errors.
func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		fmt.Fprintf(a.stderr, "Usage of backecho %s:\n", name)
		fs.PrintDefaults()
	}
	return fs
}

// parse runs fs.Parse and maps failures to an exit code; ok is false when
// the caller should return code.
func parse(fs *flag.FlagSet, args []string) (code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "backecho: unexpected argument %q\n", fs.Arg(0))
		return exitUsage, false
	}
	return 0, true
}

// errNotRepo is a usage error: backecho only runs inside a git repository.
var errNotRepo = errors.New("not a git repository (or any parent up to the filesystem root)")

// gitToplevel walks up from dir to the directory holding .git (a directory,
// or a file for worktrees and submodules). It does not shell out to git.
func gitToplevel(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errNotRepo
		}
		dir = parent
	}
}

// dataDir resolves --dir (relative to cwd), then BACKECHO_DIR (relative to
// the toplevel), then .backecho at the toplevel.
func (a *app) dataDir(flagDir, root string) string {
	switch {
	case flagDir != "":
		if filepath.IsAbs(flagDir) {
			return filepath.Clean(flagDir)
		}
		return filepath.Join(a.cwd, flagDir)
	case a.env("BACKECHO_DIR") != "":
		d := a.env("BACKECHO_DIR")
		if filepath.IsAbs(d) {
			return filepath.Clean(d)
		}
		return filepath.Join(root, d)
	}
	return filepath.Join(root, ".backecho")
}

// repo resolves the toplevel and data dir, printing the error on failure.
func (a *app) repo(flagDir string) (root, dir string, code int, ok bool) {
	root, err := gitToplevel(a.cwd)
	if err != nil {
		fmt.Fprintln(a.stderr, "backecho:", err)
		return "", "", exitUsage, false
	}
	return root, a.dataDir(flagDir, root), 0, true
}

func (a *app) logf(format string, args ...any) {
	fmt.Fprintf(a.stderr, "backecho: "+format+"\n", args...)
}

// relTo shows p relative to base when it is inside it.
func relTo(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}

func (a *app) path(args []string) int {
	fs := a.flags("path")
	dirFlag := fs.String("dir", "", "data directory (default .backecho at the git toplevel)")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	_, dir, code, ok := a.repo(*dirFlag)
	if !ok {
		return code
	}
	fmt.Fprintln(a.stdout, dir)
	return exitOK
}
