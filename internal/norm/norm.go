// Package norm turns raw events into agent-blind observations: it redacts,
// normalizes, fingerprints, and decides which results are facts.
package norm

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/euryopa/backecho/internal/model"
)

// Normalized is a shell command reduced to its identity.
type Normalized struct {
	Command string   // normalized command text, redacted
	Cwd     string   // slash-separated, relative to the git toplevel, "." for the root
	Env     []string // names of leading env assignments; values are dropped
	ID      string   // fingerprint
	Class   string   // model.ClassVerify or model.ClassLocal
}

// Fingerprint is the identity of an entry: sha256 of cwd + "\n" + command,
// first 12 hex chars. Agent is not part of it.
func Fingerprint(cwd, cmd string) string {
	sum := sha256.Sum256([]byte(cwd + "\n" + cmd))
	return hex.EncodeToString(sum[:])[:12]
}

var (
	envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	tmpRe       = regexp.MustCompile(`(?:/private)?/(?:tmp|var/folders)/[^\s'"]*`)
	portFlagRe  = regexp.MustCompile(`(--port[= ])(\d{4,5})\b`)
	colonPortRe = regexp.MustCompile(`:(\d{4,5})\b`)
	trailTrueRe = regexp.MustCompile(`(?:\s*&&\s*true)+$`)
	absPathRe   = regexp.MustCompile(`(?:^|[\s=:'"(])(?:/[A-Za-z0-9_.-]|[A-Za-z]:[\\/])`)
)

// Normalize reduces a shell command recorded in cwd. ok is false when the
// command ran outside the git toplevel.
func Normalize(cmd, cwd, home, root string) (n Normalized, ok bool) {
	toks := tokenize(strings.TrimSpace(cmd))

	// 1. Strip leading env assignments, and `env` used as a prefix.
	for len(toks) > 0 {
		t := toks[0]
		if t == "env" && len(toks) > 1 && envAssignRe.MatchString(toks[1]) {
			toks = toks[1:]
			continue
		}
		if !envAssignRe.MatchString(t) {
			break
		}
		n.Env = append(n.Env, t[:strings.IndexByte(t, '=')])
		toks = toks[1:]
	}

	// A leading `cd DIR &&` moves the effective cwd when DIR stays in the repo.
	if cwd == "" {
		cwd = root
	}
	for len(toks) >= 3 && toks[0] == "cd" && toks[2] == "&&" {
		dir := expandHome(unquote(toks[1]), home)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		if _, in := Rel(root, dir); !in {
			break
		}
		cwd = dir
		toks = toks[3:]
	}

	rel, in := Rel(root, cwd)
	if !in {
		return n, false
	}
	n.Cwd = rel

	// 2–5. Paths, temp dirs, ports, whitespace (tokenize already collapsed it).
	s := strings.Join(toks, " ")
	s = Redact(s)
	s = replacePathPrefix(s, root, ".")
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		s = replacePathPrefix(s, real, ".")
	}
	if home != "" {
		s = replacePathPrefix(s, home, "~")
	}
	s = strings.NewReplacer("${HOME}", "~", "$HOME", "~").Replace(s)
	s = tmpRe.ReplaceAllString(s, "<tmp>")
	s = portFlagRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := portFlagRe.FindStringSubmatch(m)
		if inPortRange(sub[2]) {
			return sub[1] + ":PORT"
		}
		return m
	})
	s = colonPortRe.ReplaceAllStringFunc(s, func(m string) string {
		if inPortRange(m[1:]) {
			return ":PORT"
		}
		return m
	})
	s = trailTrueRe.ReplaceAllString(s, "")
	n.Command = strings.TrimSpace(s)

	n.ID = Fingerprint(n.Cwd, n.Command)
	n.Class = classOf(n.Command, n.Env)
	return n, true
}

func inPortRange(s string) bool {
	p, err := strconv.Atoi(s)
	return err == nil && p >= 1024 && p <= 65535
}

func classOf(cmd string, env []string) string {
	if len(env) > 0 || strings.Contains(cmd, ":PORT") || strings.Contains(cmd, "<tmp>") ||
		absPathRe.MatchString(cmd) || hasHomeRef(cmd) {
		return model.ClassLocal
	}
	return model.ClassVerify
}

func hasHomeRef(cmd string) bool {
	for _, t := range strings.Fields(cmd) {
		t = strings.TrimLeft(t, `"'=(`)
		if t == "~" || strings.HasPrefix(t, "~/") {
			return true
		}
		if i := strings.Index(t, "=~"); i >= 0 {
			return true
		}
	}
	return false
}

// replacePathPrefix replaces an absolute directory prefix with a short form,
// only at path boundaries.
func replacePathPrefix(s, dir, short string) string {
	dir = strings.TrimRight(dir, "/\\")
	if dir == "" || dir == "/" {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, dir)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(dir)
		boundaryAfter := end == len(s) || strings.ContainsRune("/\\ '\":;", rune(s[end]))
		boundaryBefore := i == 0 || strings.ContainsRune(" '\"=:(", rune(s[i-1]))
		b.WriteString(s[:i])
		if boundaryAfter && boundaryBefore {
			b.WriteString(short)
		} else {
			b.WriteString(dir)
		}
		s = s[end:]
	}
}

func expandHome(p, home string) string {
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/")) {
		return filepath.Join(home, p[1:])
	}
	return p
}

// Rel returns p relative to root in slash form and whether p is inside root.
// Both sides are compared as recorded and with symlinks resolved, so macOS
// /var and /private/var agree.
func Rel(root, p string) (string, bool) {
	if p == "" || root == "" {
		return "", false
	}
	p = fromGitBash(p)
	if !filepath.IsAbs(p) {
		return "", false
	}
	try := func(r, q string) (string, bool) {
		rel, err := filepath.Rel(r, q)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return "", false
		}
		return filepath.ToSlash(rel), true
	}
	if rel, ok := try(root, p); ok {
		return rel, true
	}
	rr, err1 := filepath.EvalSymlinks(root)
	rp := evalExisting(p)
	if err1 == nil {
		if rel, ok := try(rr, rp); ok {
			return rel, true
		}
	}
	if filepath.Separator == '\\' && strings.EqualFold(filepath.Clean(root), filepath.Clean(p)) {
		return ".", true
	}
	return "", false
}

// evalExisting resolves symlinks on the longest existing prefix of p.
func evalExisting(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// fromGitBash maps /c/Users/x to C:\Users\x on Windows.
func fromGitBash(p string) string {
	if filepath.Separator != '\\' || len(p) < 3 || p[0] != '/' || p[2] != '/' {
		return p
	}
	return strings.ToUpper(p[1:2]) + `:\` + filepath.FromSlash(p[3:])
}

// RelPath maps a recorded edit path to a repo-relative slash path. Relative
// paths are taken relative to cwd (or the root when cwd is unknown).
func RelPath(root, cwd, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	if strings.HasPrefix(p, "file://") {
		p = strings.TrimPrefix(p, "file://")
	}
	if !filepath.IsAbs(fromGitBash(p)) {
		base := cwd
		if base == "" {
			base = root
		}
		p = filepath.Join(base, p)
	}
	rel, ok := Rel(root, p)
	if !ok || rel == "." {
		return "", false
	}
	return rel, true
}

// tokenize splits a command on unquoted whitespace and emits the operators
// &&, ||, |, ; and & as their own tokens. Quotes are preserved in tokens.
func tokenize(s string) []string {
	var toks []string
	var cur strings.Builder
	var quote byte
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == '\\' && quote == '"' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			i++
			cur.WriteByte(s[i])
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == ';':
			flush()
			toks = append(toks, ";")
		case c == '|':
			flush()
			if i+1 < len(s) && s[i+1] == '|' {
				toks = append(toks, "||")
				i++
			} else {
				toks = append(toks, "|")
			}
		case c == '&':
			prev := byte(0)
			if cur.Len() > 0 {
				prev = cur.String()[cur.Len()-1]
			}
			if prev == '>' || prev == '<' || (i+1 < len(s) && s[i+1] == '>') {
				cur.WriteByte(c)
				continue
			}
			flush()
			if i+1 < len(s) && s[i+1] == '&' {
				toks = append(toks, "&&")
				i++
			} else {
				toks = append(toks, "&")
			}
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return toks
}

func unquote(t string) string {
	if len(t) >= 2 && (t[0] == '"' || t[0] == '\'') && t[len(t)-1] == t[0] {
		return t[1 : len(t)-1]
	}
	return t
}

func isOp(t string) bool {
	return t == "&&" || t == "||" || t == "|" || t == ";" || t == "&"
}

// segments splits a normalized command into simple commands and the
// operators between them. Redirections are removed and words unquoted.
func segments(cmd string) (segs [][]string, ops []string) {
	var cur []string
	toks := tokenize(cmd)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if isOp(t) {
			segs = append(segs, cur)
			ops = append(ops, t)
			cur = nil
			continue
		}
		if t == ">" || t == ">>" || t == "<" || t == "2>" || t == "2>>" || t == "&>" {
			i++ // skip the redirection target
			continue
		}
		if strings.ContainsAny(t[:1], "<>") || (len(t) > 1 && (t[0] >= '0' && t[0] <= '9') && strings.ContainsAny(t[1:2], "<>")) || strings.HasPrefix(t, "&>") {
			continue
		}
		cur = append(cur, unquote(t))
	}
	segs = append(segs, cur)
	return segs, ops
}

// Tokens returns the words two commands may share to count as the same
// procedure: path segments, test names, package names.
func Tokens(cmd string) map[string]bool {
	out := map[string]bool{}
	segs, _ := segments(cmd)
	for _, seg := range segs {
		seg = stripWrappers(seg)
		for i, w := range seg {
			if i == 0 || strings.HasPrefix(w, "-") || genericWord[w] {
				if eq := strings.IndexByte(w, '='); i > 0 && eq > 0 && strings.HasPrefix(w, "-") {
					w = w[eq+1:] // --package=api
				} else {
					continue
				}
			}
			addToken(out, w)
			for _, part := range strings.FieldsFunc(w, func(r rune) bool { return r == '/' || r == '\\' || r == ':' }) {
				addToken(out, part)
				if ext := filepath.Ext(part); ext != "" && ext != part {
					addToken(out, strings.TrimSuffix(part, ext))
				}
			}
		}
	}
	return out
}

func addToken(m map[string]bool, w string) {
	w = strings.Trim(w, `"'.,()`)
	if len(w) < 3 || genericWord[w] || w == "..." {
		return
	}
	m[w] = true
}

var genericWord = map[string]bool{
	"test": true, "tests": true, "run": true, "build": true, "lint": true, "check": true,
	"exec": true, "--": true, "true": true, "false": true, "src": true, "lib": true,
	"cmd": true, "internal": true, "pkg": true, "app": true, "./...": true, "...": true,
	"dev": true, "npm": true, "npx": true, "node": true, "python": true, "python3": true,
	"spec": true, "<tmp>": true, "~": true, ":PORT": true,
}

// SharedToken reports whether two normalized commands are about the same
// thing: identical text, the same verb and subcommand, or a common token.
func SharedToken(a, b string) bool {
	if a == b {
		return true
	}
	sa, _ := segments(a)
	sb, _ := segments(b)
	if len(sa) > 0 && len(sb) > 0 {
		x, y := stripWrappers(sa[len(sa)-1]), stripWrappers(sb[len(sb)-1])
		if len(x) >= 2 && len(y) >= 2 && x[0] == y[0] && x[1] == y[1] && !strings.HasPrefix(x[1], "-") {
			return true
		}
	}
	ta, tb := Tokens(a), Tokens(b)
	for t := range ta {
		if tb[t] {
			return true
		}
	}
	return false
}

func sortedUnion(a, b []string, limit int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Union merges two string sets, sorted, capped at limit when limit > 0.
func Union(a, b []string, limit int) []string { return sortedUnion(a, b, limit) }
