package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/scan"
)

var now = time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)

type sandbox struct {
	t          *testing.T
	home, repo string
	env        map[string]string
}

// newSandbox makes a fake home and a git repo, and chdirs into the repo.
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	base := t.TempDir()
	if r, err := filepath.EvalSymlinks(base); err == nil {
		base = r
	}
	s := &sandbox{t: t, home: filepath.Join(base, "home"), repo: filepath.Join(base, "work", "app")}
	for _, d := range []string{s.home, filepath.Join(s.repo, ".git"), filepath.Join(s.repo, "src")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.env = map[string]string{"HOME": s.home}
	t.Chdir(s.repo)
	return s
}

func (s *sandbox) run(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(args, &out, &errb, func(k string) string { return s.env[k] }, now)
	return code, out.String(), errb.String()
}

// placeFixture copies a testdata fixture into the fake home, expanding the
// placeholders the adapter tests use.
func (s *sandbox) placeFixture(src, dst string) {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", src))
	if err != nil {
		// t.Chdir moved us; resolve from this file instead.
		_, file, _, _ := runtime.Caller(0)
		b, err = os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "testdata", src))
		if err != nil {
			s.t.Fatal(err)
		}
	}
	esc := func(p string) string { j, _ := json.Marshal(p); return string(j[1 : len(j)-1]) }
	text := strings.NewReplacer(
		"{{ROOT}}", esc(s.repo),
		"{{HOME}}", esc(s.home),
		"{{ELSEWHERE}}", esc(filepath.Join(filepath.Dir(s.repo), "other")),
	).Replace(string(b))
	p := filepath.Join(s.home, filepath.FromSlash(dst))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sandbox) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(s.repo, filepath.FromSlash(rel)))
	return string(b)
}

func TestSyncEndToEnd(t *testing.T) {
	s := newSandbox(t)
	os.WriteFile(filepath.Join(s.repo, ".gitignore"), []byte("node_modules/\n"), 0o644)
	s.placeFixture("claude/session.jsonl", ".claude/projects/-work-app/s1.jsonl")
	s.placeFixture("codex/rollout.jsonl", ".codex/sessions/2026/10/04/rollout-2026-10-04T14-00-00-x.jsonl")

	code, _, stderr := s.run("sync")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	verify := s.read(".backecho/verify.md")
	for _, want := range []string{"- `npm test`", "via: claude", "- `cargo test -p api`", "via: codex", "after: src/auth.ts", "was: Error: Cannot find module"} {
		if !strings.Contains(verify, want) {
			t.Errorf("verify.md missing %q:\n%s", want, verify)
		}
	}
	for _, leak := range []string{"SECRETTOKEN123", "test-unknown", "test-elsewhere", "ls -la"} {
		if strings.Contains(verify, leak) || strings.Contains(s.read(".backecho/store.json"), leak) {
			t.Errorf("%q leaked into output", leak)
		}
	}
	if !strings.Contains(s.read(".gitignore"), ".backecho/local.md") {
		t.Errorf("gitignore not updated:\n%s", s.read(".gitignore"))
	}

	// Spec test 11: a second sync on unchanged files yields no new events.
	storeBefore := s.read(".backecho/store.json")
	code, _, stderr = s.run("sync")
	if code != 0 || !strings.Contains(stderr, "nothing new") {
		t.Fatalf("second sync: exit %d\n%s", code, stderr)
	}
	if s.read(".backecho/store.json") != storeBefore || s.read(".backecho/verify.md") != verify {
		t.Fatal("unchanged logs changed the store")
	}
	if strings.Count(s.read(".gitignore"), ".backecho/local.md") != 1 {
		t.Fatal("gitignore line appended twice")
	}

	// Appending to a log counts only the new records.
	f, _ := os.OpenFile(filepath.Join(s.home, ".claude/projects/-work-app/s1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("\n" + `{"cwd":"` + s.repo + `","sessionId":"7f1c2a90-1111-4c8e-9a51-000000000001","type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_90","name":"Bash","input":{"command":"npm test"}}]},"uuid":"z1","timestamp":"2026-10-04T15:00:00.000Z"}` + "\n" +
		`{"cwd":"` + s.repo + `","sessionId":"7f1c2a90-1111-4c8e-9a51-000000000001","type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_90","content":"ok","is_error":false}]},"uuid":"z2","timestamp":"2026-10-04T15:00:09.000Z","toolUseResult":{"stdout":"ok","stderr":"","interrupted":false,"isImage":false}}` + "\n")
	f.Close()
	future := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(s.home, ".claude/projects/-work-app/s1.jsonl"), future, future)
	if code, _, stderr = s.run("sync"); code != 0 {
		t.Fatal(stderr)
	}
	var entries []model.Entry
	_, out, _ := s.run("show", "--json")
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Command == "npm test" && (e.OKCount != 2 || e.FailCount != 1 || e.Sessions != 1) {
			t.Errorf("npm test after append: %+v", e)
		}
	}
}

func TestSyncDryRunWritesNothing(t *testing.T) {
	s := newSandbox(t)
	s.placeFixture("claude/session.jsonl", ".claude/projects/x/s1.jsonl")
	code, out, _ := s.run("sync", "--dry-run")
	if code != 0 || !strings.Contains(out, "- `npm test`") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(s.repo, ".backecho")); err == nil {
		t.Fatal("dry run wrote the data dir")
	}
}

func TestSyncWithoutGitignoreWarnsOnce(t *testing.T) {
	s := newSandbox(t)
	_, _, stderr := s.run("sync")
	if !strings.Contains(stderr, "no .gitignore") {
		t.Fatalf("no warning:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(s.repo, ".gitignore")); err == nil {
		t.Fatal(".gitignore created")
	}
	_, _, stderr = s.run("sync")
	if strings.Contains(stderr, "no .gitignore") {
		t.Fatal("warned twice")
	}
}

func TestLocalEntriesStayLocal(t *testing.T) {
	s := newSandbox(t)
	s.placeFixture("claude/session.jsonl", ".claude/projects/x/s1.jsonl")
	f, _ := os.OpenFile(filepath.Join(s.home, ".claude/projects/x/s1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("\n" + `{"cwd":"` + s.repo + `","sessionId":"s9","type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"API_KEY=sk-123 PORT=4000 npm run e2e"}}]},"uuid":"a","timestamp":"2026-10-04T15:00:00Z"}` + "\n" +
		`{"cwd":"` + s.repo + `","sessionId":"s9","type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"uuid":"b","timestamp":"2026-10-04T15:00:01Z","toolUseResult":{"stdout":"ok","stderr":"","interrupted":false}}` + "\n")
	f.Close()
	if code, _, stderr := s.run("sync"); code != 0 {
		t.Fatal(stderr)
	}
	local := s.read(".backecho/local.md")
	if !strings.Contains(local, "- `npm run e2e`") || !strings.Contains(local, "env: API_KEY, PORT") {
		t.Fatalf("local.md:\n%s", local)
	}
	for _, f := range []string{".backecho/local.md", ".backecho/verify.md", ".backecho/store.json", ".backecho/local.json"} {
		if strings.Contains(s.read(f), "sk-123") || strings.Contains(s.read(f), "4000") {
			t.Errorf("env value written to %s", f)
		}
	}
	if strings.Contains(s.read(".backecho/verify.md"), "e2e") {
		t.Error("local entry in verify.md")
	}
}

func TestStatusListsEveryAdapter(t *testing.T) {
	s := newSandbox(t)
	s.placeFixture("claude/session.jsonl", ".claude/projects/x/s1.jsonl")
	code, out, _ := s.run("status", "--json")
	if code != 0 {
		t.Fatal(out)
	}
	var st []model.AdapterStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if len(st) != len(scan.All()) {
		t.Fatalf("status lists %d adapters, want %d", len(st), len(scan.All()))
	}
	for _, a := range st {
		switch a.State {
		case "found", "empty", "skipped":
		default:
			t.Errorf("%s: state %q", a.Agent, a.State)
		}
		if a.Agent == "claude" && (a.State != "found" || a.Events == 0) {
			t.Errorf("claude: %+v", a)
		}
	}
	code, out, _ = s.run("status")
	if code != 0 || !strings.Contains(out, "AGENT") || !strings.Contains(out, "claude") {
		t.Fatalf("table:\n%s", out)
	}
}

func TestUsageAndNotARepo(t *testing.T) {
	s := newSandbox(t)
	if code, _, _ := s.run(); code != exitUsage {
		t.Errorf("no args: exit %d", code)
	}
	if code, _, _ := s.run("frobnicate"); code != exitUsage {
		t.Errorf("unknown command: exit %d", code)
	}
	if code, _, _ := s.run("sync", "--agent", "nope"); code != exitUsage {
		t.Errorf("unknown agent: exit %d", code)
	}
	if code, _, _ := s.run("sync", "--since", "yesterday"); code != exitUsage {
		t.Errorf("bad since: exit %d", code)
	}
	if code, out, _ := s.run("help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("help: exit %d", code)
	}
	t.Chdir(s.home)
	if code, _, stderr := s.run("sync"); code != exitUsage || !strings.Contains(stderr, "not a git repository") {
		t.Errorf("outside a repo: exit %d %s", code, stderr)
	}
}

func TestPathAndDirOverride(t *testing.T) {
	s := newSandbox(t)
	_, out, _ := s.run("path")
	if strings.TrimSpace(out) != filepath.Join(s.repo, ".backecho") {
		t.Errorf("path = %q", out)
	}
	s.env["BACKECHO_DIR"] = "notes/agents"
	_, out, _ = s.run("path")
	if strings.TrimSpace(out) != filepath.Join(s.repo, "notes", "agents") {
		t.Errorf("BACKECHO_DIR path = %q", out)
	}
	_, out, _ = s.run("path", "--dir", "elsewhere")
	if strings.TrimSpace(out) != filepath.Join(s.repo, "elsewhere") {
		t.Errorf("--dir path = %q", out)
	}
}

func TestSubdirectoryFindsToplevel(t *testing.T) {
	s := newSandbox(t)
	t.Chdir(filepath.Join(s.repo, "src"))
	_, out, _ := s.run("path")
	if strings.TrimSpace(out) != filepath.Join(s.repo, ".backecho") {
		t.Errorf("path from subdir = %q", out)
	}
}

func TestStale(t *testing.T) {
	s := newSandbox(t)
	s.placeFixture("claude/session.jsonl", ".claude/projects/x/s1.jsonl")
	s.run("sync")
	var out bytes.Buffer
	code := run([]string{"stale"}, &out, &bytes.Buffer{}, func(k string) string { return s.env[k] }, now.AddDate(0, 2, 0))
	if code != 0 || !strings.Contains(out.String(), "`npm test`") {
		t.Fatalf("stale:\n%s", out.String())
	}
}
