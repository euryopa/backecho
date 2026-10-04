package norm

import (
	"path/filepath"
	"regexp"
	"strings"
)

const redacted = "<redacted>"

var redactRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Authorization header: keep the key, drop the scheme and credential.
	{regexp.MustCompile(`(?i)(authorization\s*:\s*)(?:(?:bearer|basic|token|digest)\s+)?[^\s'"()\[\]{},;]+`), "${1}" + redacted},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`), "${1}" + redacted},
	// key=value and key: value for anything that looks like a credential name.
	{regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:token|api[_-]?key|secret|password|passwd)[A-Za-z0-9_]*)(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s&'",;()\[\]{}]+)`), "${1}${2}" + redacted},
	// URL userinfo, which also covers postgres://, mysql://, mongodb://, redis://.
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@'"]+@`), "${1}" + redacted + "@"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), redacted},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`), redacted},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), redacted},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), redacted},
}

var pemRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)

// Redact drops credential values and keeps their keys. It runs on command text
// and on failure hints before anything is written.
func Redact(s string) string {
	if pemRe.MatchString(s) {
		return redacted
	}
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

// Head returns the first non-empty line of s, stripped of ANSI escapes,
// capped at 200 runes and redacted.
func Head(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		line = Redact(line)
		if r := []rune(line); len(r) > 200 {
			line = string(r[:200])
		}
		return line
	}
	return ""
}

// SensitivePath reports whether a path names a file whose contents must never
// reach the store: .env, .env.*, *.pem, id_rsa and friends.
func SensitivePath(p string) bool {
	p = strings.Trim(p, `"'`)
	if p == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(p, `\`, "/")))
	switch {
	case base == ".env", strings.HasPrefix(base, ".env."):
		return true
	case strings.HasSuffix(base, ".pem"), strings.HasSuffix(base, ".key"):
		return true
	case strings.HasPrefix(base, "id_rsa"), strings.HasPrefix(base, "id_ed25519"), strings.HasPrefix(base, "id_ecdsa"), strings.HasPrefix(base, "id_dsa"):
		return true
	}
	return false
}

// SensitiveCommand reports whether any token of the command reads a sensitive
// file. It prefers false positives: the event is dropped.
func SensitiveCommand(cmd string) bool {
	for _, tok := range strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '=' || r == '<' || r == '>' || r == '|' || r == ';' || r == '&' || r == '(' || r == ')'
	}) {
		if SensitivePath(tok) {
			return true
		}
	}
	return false
}
