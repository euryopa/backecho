package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
	"github.com/euryopa/backecho/internal/render"
	"github.com/euryopa/backecho/internal/store"
)

// Every fixture plants these. None may survive into rendered output.
const (
	plantedSecret = "SECRETTOKEN123"
	unknownExit   = "make test-unknown"   // a call with no recorded exit status
	elsewhereCmd  = "make test-elsewhere" // a session outside the repo
)

// testdataDir is the repo-level testdata directory.
func testdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

// env is a fake environment: only the given keys are set.
func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// layout is a test sandbox: a fake home and a fake repo root.
type layout struct {
	Home, Root string
}

func newLayout(t *testing.T) layout {
	t.Helper()
	base := t.TempDir()
	if r, err := filepath.EvalSymlinks(base); err == nil {
		base = r
	}
	l := layout{Home: filepath.Join(base, "home"), Root: filepath.Join(base, "work", "app")}
	for _, d := range []string{l.Home, l.Root} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// jsonEscape makes a path safe to splice into a JSON string literal.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// expand replaces {{ROOT}}, {{HOME}} and {{ELSEWHERE}} in fixture text.
func (l layout) expand(s string) string {
	return strings.NewReplacer(
		"{{ROOT}}", jsonEscape(l.Root),
		"{{HOME}}", jsonEscape(l.Home),
		"{{ELSEWHERE}}", jsonEscape(filepath.Join(filepath.Dir(l.Root), "other")),
		"{{ROOT_URI}}", "file://"+filepath.ToSlash(l.Root),
	).Replace(s)
}

// place copies testdata/<src> to <home>/<dst> with placeholders expanded.
func (l layout) place(t *testing.T, src, dst string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdataDir(t), src))
	if err != nil {
		t.Fatal(err)
	}
	dst = filepath.Join(l.Home, filepath.FromSlash(l.expand(dst)))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(l.expand(string(b))), 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

// collect runs Discover and Parse for one adapter, like `backecho sync` does.
func collect(t *testing.T, a Adapter, l layout, e func(string) string) (Status, []model.Event, int) {
	t.Helper()
	st, srcs := a.Discover(l.Home, e)
	if ra, ok := a.(RepoAdapter); ok {
		srcs = append(srcs, ra.DiscoverRepo(l.Root)...)
	}
	var events []model.Event
	unrecognizedFiles := 0
	for _, s := range srcs {
		evs, err := a.Parse(s, l.Root, time.Time{})
		if err != nil {
			if strings.Contains(err.Error(), ErrUnrecognized.Error()) {
				unrecognizedFiles++
				continue
			}
			t.Fatalf("%s: parse %s: %v", a.ID(), s.Path, err)
		}
		events = append(events, evs...)
	}
	return st, events, unrecognizedFiles
}

// contract is what every adapter fixture must prove.
type contract struct {
	// paired is the command that failed, was edited around, then passed.
	// Empty for an adapter whose format cannot prove an exit status: then
	// the contract asserts that nothing becomes an entry.
	paired string
	after  string // the edit path between failure and success
}

// checkContract runs the whole pipeline on the events and checks the
// acceptance rules every adapter shares.
func checkContract(t *testing.T, agent string, l layout, events []model.Event, c contract) {
	t.Helper()
	for _, e := range events {
		if e.Agent != agent {
			t.Errorf("event from %q in %s adapter", e.Agent, agent)
		}
		if e.Kind == model.KindShell && strings.Contains(e.Command, elsewhereCmd) {
			t.Errorf("event from a session outside the repo: %+v", e)
		}
	}
	obs, res := norm.Keep(events, nil, l.Home, l.Root)
	st := &model.Store{Version: store.Version}
	store.Merge(st, obs, res)

	out := string(render.Verify(st)) + string(render.Local(st))
	js, _ := store.Marshal(st)
	if strings.Contains(out, plantedSecret) || strings.Contains(string(js), plantedSecret) {
		t.Errorf("planted secret leaked:\n%s", out)
	}
	for _, e := range st.Entries {
		if strings.Contains(e.Command, "test-unknown") {
			t.Errorf("nil exit became an entry: %+v", e)
		}
		if strings.Contains(e.Command, "test-elsewhere") {
			t.Errorf("other repo became an entry: %+v", e)
		}
		if strings.HasPrefix(e.Command, "ls") {
			t.Errorf("denylisted command kept: %+v", e)
		}
	}

	if c.paired == "" {
		if len(st.Entries) != 0 {
			t.Errorf("adapter without a provable exit produced entries: %+v", st.Entries)
		}
		return
	}
	var got *model.Entry
	for i := range st.Entries {
		if st.Entries[i].Command == c.paired {
			got = &st.Entries[i]
		}
	}
	if got == nil {
		t.Fatalf("no entry for %q; events:\n%s\nrendered:\n%s", c.paired, dump(events), out)
	}
	if got.FailCount < 1 || got.OKCount < 1 {
		t.Errorf("entry not paired: %+v", got)
	}
	if c.after != "" && !strings.Contains(strings.Join(got.After, ","), c.after) {
		t.Errorf("after = %v, want %s", got.After, c.after)
	}
	if strings.Join(got.Agents, ",") != agent {
		t.Errorf("agents = %v", got.Agents)
	}
	if !strings.Contains(out, "via: "+agent) {
		t.Errorf("via does not name %s:\n%s", agent, out)
	}
}

func dump(events []model.Event) string {
	var b strings.Builder
	for _, e := range events {
		exit := "nil"
		if e.Exit != nil {
			exit = strconv.Itoa(*e.Exit)
		}
		b.WriteString("  " + string(e.Kind) + " " + e.Session + " cwd=" + e.Cwd + " cmd=" + e.Command + " path=" + e.Path + " exit=" + exit + "\n")
	}
	return b.String()
}

func TestAllRegistersEveryAdapter(t *testing.T) {
	want := []string{"claude", "codex", "cursor", "copilot", "opencode", "gemini", "amp", "goose", "hermes", "pi", "cline", "aider", "kimi", "qwen", "grok", "antigravity", "continue", "droid", "openclaw", "crush", "cursor-ide"}
	got := IDs()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("All() = %v\nwant    %v", got, want)
	}
}

// Spec test 14: a missing agent root does not fail and reports empty.
func TestSpec14_MissingRootsAreEmpty(t *testing.T) {
	l := newLayout(t)
	for _, a := range All() {
		st, srcs := a.Discover(l.Home, env(nil))
		if len(srcs) != 0 {
			t.Errorf("%s: found sources in an empty home: %v", a.ID(), srcs)
		}
		if st.State != "empty" {
			t.Errorf("%s: state %q (%s), want empty", a.ID(), st.State, st.Detail)
		}
		if st.Agent != a.ID() {
			t.Errorf("%s: status agent %q", a.ID(), st.Agent)
		}
	}
}

// Spec test 10: a truncated JSONL line is skipped.
func TestSpec10_TruncatedLineSkipped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.jsonl")
	os.WriteFile(p, []byte("{\"a\":1}\n{\"b\":\n{\"c\":3}\nnot json\n{\"d\":"), 0o644)
	var n int
	good, bad, err := scanJSONL(p, func(obj) { n++ })
	if err != nil || good != 2 || bad != 3 || n != 2 {
		t.Fatalf("good=%d bad=%d n=%d err=%v", good, bad, n, err)
	}
}

func TestToolKind(t *testing.T) {
	cases := map[string]model.Kind{
		"Bash": model.KindShell, "mcp__desktop__shell": model.KindShell, "functions.exec": model.KindShell,
		"run_terminal_cmd": model.KindShell, "PowerShell": model.KindShell, "Edit": model.KindEdit,
		"MultiEdit": model.KindEdit, "apply_patch": model.KindEdit, "StrReplace": model.KindEdit,
		"Read": "", "Grep": "",
	}
	for name, want := range cases {
		if got := toolKind(name, nil); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestShellCommand(t *testing.T) {
	if got := shellCommand([]any{"bash", "-lc", "npm test"}); got != "npm test" {
		t.Errorf("got %q", got)
	}
	if got := shellCommand([]any{"go", "test", "./..."}); got != "go test ./..." {
		t.Errorf("got %q", got)
	}
}

func TestFilterRepoMatchesByEditWhenNoCwd(t *testing.T) {
	root := "/work/app"
	if runtime.GOOS == "windows" {
		t.Skip("posix paths")
	}
	events := []model.Event{
		{Session: "a", Kind: model.KindShell, Command: "npm test", Exit: model.IntPtr(0)},
		{Session: "a", Kind: model.KindEdit, Path: "/work/app/src/x.ts"},
		{Session: "b", Kind: model.KindShell, Command: "npm test", Exit: model.IntPtr(0)},
		{Session: "b", Kind: model.KindEdit, Path: "relative/only.ts"},
		{Session: "c", Kind: model.KindShell, Command: "npm test", Cwd: "/work/app-two", Exit: model.IntPtr(0)},
	}
	got := filterRepo(events, root, time.Time{})
	if len(got) != 2 || got[0].Session != "a" {
		t.Fatalf("got %+v", got)
	}
}
