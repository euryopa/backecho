package norm

import (
	"path/filepath"
	"strings"
)

var denyFirst = map[string]bool{
	"ls": true, "ll": true, "cat": true, "head": true, "tail": true, "less": true,
	"pwd": true, "echo": true, "printf": true, "cd": true, "true": true, "false": true,
	"which": true, "whoami": true, "env": true, "printenv": true, "export": true, "set": true,
}

var denyGit = map[string]bool{"status": true, "diff": true, "log": true, "show": true, "branch": true}

// Shape describes what a normalized command can prove.
type Shape struct {
	Denied      bool // denylisted, dropped even on exit 0
	LongRunning bool // dev server, watcher, or backgrounded
	Unreliable  bool // pipeline, ||, ; — the exit status is not the command's
	ProjectVerb bool // a known verify verb: test, lint, build, check, typecheck, migrate
}

// Analyze classifies a normalized command.
func Analyze(cmd string) Shape {
	var sh Shape
	segs, ops := segments(cmd)
	for _, op := range ops {
		switch op {
		case "&":
			sh.LongRunning = true
		case "|", "||", ";":
			sh.Unreliable = true
		}
	}
	if len(segs) > 0 && len(segs[len(segs)-1]) == 0 && len(ops) > 0 && ops[len(ops)-1] == "&" {
		segs = segs[:len(segs)-1]
	}
	denied := 0
	for _, seg := range segs {
		seg = stripWrappers(seg)
		if len(seg) == 0 {
			continue
		}
		if isDenied(seg) {
			denied++
		}
		if isLongRunning(seg) {
			sh.LongRunning = true
		}
		if isProjectVerb(seg) {
			sh.ProjectVerb = true
		}
	}
	if denied > 0 && denied == countNonEmpty(segs) {
		sh.Denied = true
	}
	if len(segs) == 0 || countNonEmpty(segs) == 0 {
		sh.Denied = true
	}
	return sh
}

func countNonEmpty(segs [][]string) int {
	n := 0
	for _, s := range segs {
		if len(stripWrappers(s)) > 0 {
			n++
		}
	}
	return n
}

func isDenied(seg []string) bool {
	if denyFirst[seg[0]] {
		return true
	}
	return seg[0] == "git" && len(seg) > 1 && denyGit[seg[1]]
}

// stripWrappers removes prefixes that do not change what runs: sudo, time,
// nice, and runner wrappers like `uv run` or `bundle exec`.
func stripWrappers(seg []string) []string {
	for len(seg) > 0 {
		switch seg[0] {
		case "sudo", "time", "nice", "command", "nohup":
			seg = seg[1:]
			continue
		case "uv", "poetry", "pipenv", "pdm", "hatch":
			if len(seg) > 2 && seg[1] == "run" {
				seg = seg[2:]
				continue
			}
		case "bundle":
			if len(seg) > 2 && seg[1] == "exec" {
				seg = seg[2:]
				continue
			}
		case "pnpm", "yarn", "bun":
			if len(seg) > 2 && (seg[1] == "exec" || seg[1] == "dlx" || seg[1] == "x") {
				seg = append([]string{"npx"}, seg[2:]...)
				continue
			}
		case "bunx":
			seg = append([]string{"npx"}, seg[1:]...)
			continue
		}
		break
	}
	if len(seg) == 0 {
		return seg
	}
	first := seg[0]
	if strings.ContainsAny(first, `/\`) {
		base := filepath.Base(strings.ReplaceAll(first, `\`, "/"))
		switch base {
		case "gradlew", "gradlew.bat":
			first = "gradle"
		case "mvnw", "mvnw.cmd":
			first = "mvn"
		default:
			if strings.Contains(first, "node_modules/.bin/") {
				return append([]string{"npx", base}, seg[1:]...)
			}
			first = base
		}
	}
	switch first {
	case "python3", "py":
		first = "python"
	case "pip3":
		first = "pip"
	}
	if first != seg[0] {
		seg = append([]string{first}, seg[1:]...)
	}
	return seg
}

var verifyWords = []string{"test", "lint", "build", "check", "typecheck", "type-check", "tsc", "migrate", "vet", "clippy", "verify", "spec", "e2e"}

func hasVerifyWord(s string) bool {
	s = strings.ToLower(s)
	for _, w := range []string{"install", "dev", "start", "serve", "watch", "preview", "deploy", "publish", "release"} {
		if s == w || strings.HasPrefix(s, w+":") {
			return false
		}
	}
	for _, w := range verifyWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// valueFlags take the next word as a value in package manager CLIs.
var valueFlags = map[string]bool{
	"--filter": true, "-F": true, "-C": true, "--dir": true, "--prefix": true,
	"-w": true, "--workspace": true, "--cwd": true, "-p": true, "--package": true,
	"--manifest-path": true, "-f": true, "--file": true, "--project": true, "-j": true,
}

// firstArg returns the first positional argument after the verb.
func firstArg(seg []string) (string, int) {
	for i := 1; i < len(seg); i++ {
		w := seg[i]
		if w == "--" {
			return "", -1
		}
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "+") {
			if valueFlags[w] {
				i++
			}
			continue
		}
		if envAssignRe.MatchString(w) {
			continue
		}
		return w, i
	}
	return "", -1
}

var npxTools = map[string]bool{
	"vitest": true, "jest": true, "tsc": true, "vue-tsc": true, "eslint": true, "playwright": true,
	"prisma": true, "mocha": true, "ava": true, "biome": true, "oxlint": true, "svelte-check": true,
	"prettier": true, "tsx": false, "cypress": true, "turbo": true, "nx": true, "astro": true, "stylelint": true,
}

func isProjectVerb(seg []string) bool {
	verb := seg[0]
	arg, i := firstArg(seg)
	switch verb {
	case "npm", "pnpm", "yarn", "bun":
		if arg == "run" || arg == "run-script" || arg == "test" && verb == "bun" {
			if arg == "test" {
				return true
			}
			next, _ := firstArg(append([]string{verb}, seg[i+1:]...))
			return next != "" && hasVerifyWord(next)
		}
		if arg == "t" || arg == "tst" {
			return true
		}
		return arg != "" && hasVerifyWord(arg)
	case "npx":
		if arg == "" {
			return false
		}
		tool := arg
		if at := strings.LastIndexByte(tool, '@'); at > 0 {
			tool = tool[:at]
		}
		if !npxTools[tool] {
			return false
		}
		switch tool {
		case "prisma":
			return containsWord(seg[i+1:], "migrate", "validate", "generate")
		case "prettier":
			return containsWord(seg[i+1:], "--check", "-c")
		case "turbo", "nx", "astro":
			return anyVerifyWord(seg[i+1:])
		}
		return !containsWord(seg[i+1:], "--watch", "watch", "--ui")
	case "cargo":
		switch arg {
		case "test", "build", "check", "clippy", "nextest", "t", "b", "c":
			return true
		case "fmt":
			return containsWord(seg[i+1:], "--check")
		}
	case "go":
		switch arg {
		case "test", "build", "vet":
			return true
		}
	case "pytest", "tox", "nox", "mypy", "ruff", "pyright":
		return verb != "ruff" || arg == "check"
	case "python":
		if len(seg) > 2 && seg[1] == "-m" {
			switch seg[2] {
			case "pytest", "unittest", "mypy", "tox", "pyright":
				return true
			case "ruff":
				return len(seg) > 3 && seg[3] == "check"
			}
		}
		if arg == "manage.py" && i+1 < len(seg) {
			switch seg[i+1] {
			case "test", "migrate", "check":
				return true
			}
		}
	case "make", "just", "task":
		return arg != "" && hasVerifyWord(arg)
	case "mvn", "gradle":
		for _, w := range seg[1:] {
			switch strings.TrimPrefix(w, ":") {
			case "test", "verify", "build", "check", "compile", "lint", "assemble":
				return true
			}
			if strings.HasSuffix(w, ":test") || strings.HasSuffix(w, ":build") || strings.HasSuffix(w, ":check") {
				return true
			}
		}
	case "dotnet", "swift":
		return arg == "test" || arg == "build"
	case "composer":
		return arg != "" && (arg == "test" || hasVerifyWord(arg) || arg == "phpstan" || arg == "psalm")
	case "bundle":
		return false
	case "rspec", "rubocop", "phpunit", "phpstan":
		return true
	case "rake", "rails":
		return arg != "" && (hasVerifyWord(arg) || strings.HasPrefix(arg, "db:migrate"))
	case "mix":
		switch arg {
		case "test", "compile", "credo", "dialyzer", "ecto.migrate":
			return true
		case "format":
			return containsWord(seg[i+1:], "--check-formatted")
		}
	case "xcodebuild":
		return anyVerifyWord(seg[1:])
	case "deno":
		return arg == "test" || arg == "check" || arg == "lint"
	}
	return false
}

func containsWord(ws []string, want ...string) bool {
	for _, w := range ws {
		for _, x := range want {
			if w == x {
				return true
			}
		}
	}
	return false
}

func anyVerifyWord(ws []string) bool {
	for _, w := range ws {
		if !strings.HasPrefix(w, "-") && hasVerifyWord(w) {
			return true
		}
	}
	return false
}

func isLongRunning(seg []string) bool {
	verb := seg[0]
	arg, i := firstArg(seg)
	if containsWord(seg[1:], "--watch", "--watchAll", "--watch-all", "--serve") {
		return true
	}
	switch verb {
	case "vite", "next", "nuxt", "astro", "remix", "webpack-dev-server", "nodemon", "live-server", "http-server", "serve", "uvicorn", "gunicorn", "flask", "jupyter", "watchexec", "air", "reflex", "storybook", "expo", "ng":
		switch arg {
		case "build", "lint", "check", "test", "export", "routes":
			return false
		}
		return true
	case "tail":
		return containsWord(seg[1:], "-f", "-F")
	case "cargo":
		return arg == "watch" || arg == "run"
	case "npm", "pnpm", "yarn", "bun":
		name := arg
		if arg == "run" || arg == "run-script" {
			name, _ = firstArg(append([]string{verb}, seg[i+1:]...))
		}
		switch strings.SplitN(name, ":", 2)[0] {
		case "dev", "start", "serve", "watch", "preview", "storybook":
			return true
		}
	case "npx":
		if i >= 0 && i+1 <= len(seg) {
			return isLongRunning(seg[i:])
		}
	case "docker", "podman":
		return containsWord(seg[1:], "up") && !containsWord(seg[1:], "-d", "--detach")
	case "rails":
		return arg == "s" || arg == "server"
	case "python":
		return len(seg) > 2 && seg[1] == "-m" && (seg[2] == "http.server" || seg[2] == "uvicorn" || seg[2] == "flask")
	case "tsc":
		return containsWord(seg[1:], "-w")
	case "go":
		return arg == "run"
	case "watch", "sleep", "top", "htop":
		return true
	}
	return false
}
