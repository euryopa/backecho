package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

const (
	home = "/home/dev"
	root = "/home/dev/src/app"
)

var t0 = time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC)

func sh(agent, session, cmd string, exit int, at time.Duration) model.Event {
	return model.Event{Agent: agent, Session: session, TS: t0.Add(at), Cwd: root, Kind: model.KindShell, Command: cmd, Exit: model.IntPtr(exit)}
}

func merge(st *model.Store, events ...model.Event) Result {
	obs, res := norm.Keep(events, nil, home, root)
	return Merge(st, obs, res)
}

// Spec test 4: the same command from two agents is one row.
func TestSpec04_TwoAgentsOneRow(t *testing.T) {
	st := &model.Store{Version: Version}
	merge(st, sh("claude", "a", "npm test", 0, 0))
	merge(st, sh("codex", "b", "npm test", 0, time.Hour))
	if len(st.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(st.Entries))
	}
	e := st.Entries[0]
	if e.OKCount != 2 || strings.Join(e.Agents, ",") != "claude,codex" || e.Sessions != 2 || e.LastAgent != "codex" {
		t.Fatalf("got %+v", e)
	}
}

func TestMergeFailThenSuccess(t *testing.T) {
	st := &model.Store{Version: Version}
	fail := sh("claude", "s", "npm test", 1, 0)
	fail.StderrHead = "FAIL src/a.test.ts"
	merge(st, fail, model.Event{Agent: "claude", Session: "s", TS: t0.Add(time.Minute), Cwd: root, Kind: model.KindEdit, Path: root + "/src/a.ts"}, sh("claude", "s", "npm test", 0, 2*time.Minute))
	if len(st.Entries) != 1 || len(st.Open) != 0 {
		t.Fatalf("entries %+v open %+v", st.Entries, st.Open)
	}
	e := st.Entries[0]
	if e.OKCount != 1 || e.FailCount != 1 || e.Was != "FAIL src/a.test.ts" || strings.Join(e.After, ",") != "src/a.ts" {
		t.Fatalf("got %+v", e)
	}
	if e.LastFail == nil || !e.LastFail.Equal(t0) {
		t.Fatalf("last_fail %v", e.LastFail)
	}

	// An older failure does not replace `was`; a newer one does.
	older := sh("codex", "t", "npm test", 1, -time.Hour)
	older.StderrHead = "old"
	merge(st, older)
	if st.Entries[0].Was != "FAIL src/a.test.ts" {
		t.Fatalf("older failure replaced was: %q", st.Entries[0].Was)
	}
	newer := sh("codex", "t", "npm test", 1, time.Hour)
	newer.StderrHead = "new"
	merge(st, newer)
	if st.Entries[0].Was != "new" || st.Entries[0].FailCount != 3 {
		t.Fatalf("got %+v", st.Entries[0])
	}
}

func TestMergeSameSessionCountsOnce(t *testing.T) {
	st := &model.Store{Version: Version}
	merge(st, sh("claude", "s", "npm test", 0, 0), sh("claude", "s", "npm test", 0, time.Minute))
	merge(st, sh("claude", "s", "npm test", 0, 2*time.Minute))
	if e := st.Entries[0]; e.Sessions != 1 || e.OKCount != 3 {
		t.Fatalf("got %+v", e)
	}
}

func TestOpenFailures(t *testing.T) {
	st := &model.Store{Version: Version}
	f := sh("claude", "s", "cargo test -p api", 101, 0)
	f.StderrHead = "error[E0425]: cannot find value `x`"
	merge(st, f)
	if len(st.Entries) != 0 || len(st.Open) != 1 {
		t.Fatalf("entries %+v open %+v", st.Entries, st.Open)
	}
	// The next sync sees the same failure (already counted) and a new success.
	events := []model.Event{f, sh("claude", "s", "cargo test -p api", 0, time.Minute)}
	obs, res := norm.Keep(events, []bool{true, false}, home, root)
	Merge(st, obs, res)
	if len(st.Open) != 0 || len(st.Entries) != 1 || st.Entries[0].FailCount != 1 {
		t.Fatalf("entries %+v open %+v", st.Entries, st.Open)
	}
	// Open failures expire after 7 days.
	merge(st, sh("claude", "x", "make lint", 2, 0))
	Age(st, t0.Add(8*24*time.Hour))
	if len(st.Open) != 0 {
		t.Fatalf("open not expired: %+v", st.Open)
	}
}

// Spec test 9: an entry older than 30 days is stale, and goes 30 days later.
func TestSpec09_Stale(t *testing.T) {
	st := &model.Store{Version: Version}
	merge(st, sh("claude", "s", "npm test", 0, 0))
	Age(st, t0.Add(24*time.Hour))
	if st.Entries[0].StaleSince != nil {
		t.Fatal("fresh entry marked stale")
	}
	now := t0.Add(31 * 24 * time.Hour)
	Age(st, now)
	if st.Entries[0].StaleSince == nil || !st.Entries[0].StaleSince.Equal(now) {
		t.Fatalf("not stale: %+v", st.Entries[0])
	}
	if removed := Age(st, now.Add(31*24*time.Hour)); removed != 1 || len(st.Entries) != 0 {
		t.Fatalf("not removed: %d %+v", removed, st.Entries)
	}
}

func TestStaleWhenFailuresOutnumber(t *testing.T) {
	st := &model.Store{Version: Version}
	merge(st, sh("claude", "s", "npm test", 0, 0))
	merge(st, sh("claude", "t", "npm test", 1, time.Minute), sh("claude", "u", "npm test", 1, 2*time.Minute))
	Age(st, t0.Add(time.Hour))
	if st.Entries[0].StaleSince == nil {
		t.Fatalf("not stale: %+v", st.Entries[0])
	}
}

func TestSaveLoadSplit(t *testing.T) {
	dir := t.TempDir()
	st := &model.Store{Version: Version}
	merge(st, sh("claude", "s", "npm test", 0, 0), sh("claude", "s", "PORT=3001 npm run e2e", 0, time.Minute))
	if err := Save(dir, st); err != nil {
		t.Fatal(err)
	}
	pub, _ := os.ReadFile(filepath.Join(dir, StoreFile))
	if strings.Contains(string(pub), "PORT") {
		t.Fatalf("local entry in store.json:\n%s", pub)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("got %d entries", len(loaded.Entries))
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}

func TestStatePlan(t *testing.T) {
	s := &State{Handles: map[string]*Handle{}}
	if p := s.PlanFor("claude", "/a", 10, t0); p.Skip || p.Seen != 0 {
		t.Fatalf("new handle: %+v", p)
	}
	s.Record("claude", "/a", "file", 10, t0, 4)
	if p := s.PlanFor("claude", "/a", 10, t0); !p.Skip {
		t.Fatal("unchanged handle not skipped")
	}
	if p := s.PlanFor("claude", "/a", 20, t0.Add(time.Second)); p.Skip || p.Seen != 4 {
		t.Fatalf("grown handle: %+v", p)
	}
	if p := s.PlanFor("claude", "/a", 5, t0.Add(time.Second)); p.Seen != 0 {
		t.Fatalf("shrunk handle should rescan: %+v", p)
	}
	if p := s.PlanFor("claude", "/a", 20, t0.Add(-time.Second)); p.Seen != 0 {
		t.Fatalf("mtime backwards should rescan: %+v", p)
	}
}

// Spec test 12: the gitignore line is appended once.
func TestSpec12_GitignoreOnce(t *testing.T) {
	dir := t.TempDir()
	if _, missing, _ := EnsureGitignore(dir, IgnoreLines(".backecho")); !missing {
		t.Fatal("missing .gitignore not reported")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err == nil {
		t.Fatal(".gitignore created")
	}
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules"), 0o644)
	added, _, err := EnsureGitignore(dir, IgnoreLines(".backecho"))
	if err != nil || len(added) != 3 {
		t.Fatalf("added %v err %v", added, err)
	}
	added, _, _ = EnsureGitignore(dir, IgnoreLines(".backecho"))
	if len(added) != 0 {
		t.Fatalf("appended twice: %v", added)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.Count(string(b), ".backecho/local.md") != 1 || !strings.HasPrefix(string(b), "node_modules\n") {
		t.Fatalf("got:\n%s", b)
	}
}

func TestGitignoreRespectsExistingDirRule(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".backecho/\n"), 0o644)
	if added, _, _ := EnsureGitignore(dir, IgnoreLines(".backecho")); len(added) != 0 {
		t.Fatalf("added %v", added)
	}
}

// Spec test 8: FOO=bar lands in local.md as the name only.
func TestSpec08_EnvNameOnly(t *testing.T) {
	st := &model.Store{Version: Version}
	merge(st, sh("claude", "s", "FOO=bar npm test", 0, 0))
	if len(st.Entries) != 1 {
		t.Fatalf("got %+v", st.Entries)
	}
	e := st.Entries[0]
	if e.Class != model.ClassLocal || strings.Join(e.Env, ",") != "FOO" || strings.Contains(e.Command, "bar") {
		t.Fatalf("got %+v", e)
	}
	// A plain run of the same command later proves it portable.
	merge(st, sh("codex", "t", "npm test", 0, time.Minute))
	if e := st.Entries[0]; e.Class != model.ClassVerify || len(e.Env) != 0 {
		t.Fatalf("got %+v", e)
	}
}
