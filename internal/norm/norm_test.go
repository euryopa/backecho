package norm

import (
	"strings"
	"testing"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

const (
	home = "/home/dev"
	root = "/home/dev/src/app"
)

var t0 = time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC)

func shell(agent, session, cmd string, exit *int, at time.Duration) model.Event {
	return model.Event{Agent: agent, Session: session, TS: t0.Add(at), Cwd: root, Kind: model.KindShell, Command: cmd, Exit: exit}
}

func edit(agent, session, path string, at time.Duration) model.Event {
	return model.Event{Agent: agent, Session: session, TS: t0.Add(at), Cwd: root, Kind: model.KindEdit, Path: path}
}

var zero, one = model.IntPtr(0), model.IntPtr(1)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name, cmd, cwd string
		want, wantCwd  string
		env            []string
		class          string
	}{
		{"plain", "npm   test", root, "npm test", ".", nil, model.ClassVerify},
		{"env stripped", "FOO=bar CI=1 npm test", root, "npm test", ".", []string{"FOO", "CI"}, model.ClassLocal},
		{"toplevel replaced", "go test " + root + "/pkg/...", root, "go test ./pkg/...", ".", nil, model.ClassVerify},
		{"home replaced", "cat /home/dev/notes.txt", root, "cat ~/notes.txt", ".", nil, model.ClassLocal},
		{"tmp", "pytest --basetemp=/tmp/pytest-of-dev/x", root, "pytest --basetemp=<tmp>", ".", nil, model.ClassLocal},
		{"port", "curl localhost:3000/health", root, "curl localhost:PORT/health", ".", nil, model.ClassLocal},
		{"port flag", "npm run e2e -- --port 8080", root, "npm run e2e -- --port :PORT", ".", nil, model.ClassLocal},
		{"low port kept", "nc -l :80", root, "nc -l :80", ".", nil, model.ClassVerify},
		{"trailing true", "make test && true", root, "make test", ".", nil, model.ClassVerify},
		{"subdir cwd", "cargo test -p api", root + "/crates", "cargo test -p api", "crates", nil, model.ClassVerify},
		{"leading cd", "cd " + root + "/web && npm test", root, "npm test", "web", nil, model.ClassVerify},
		{"flags not sorted", "go test -v -race ./...", root, "go test -v -race ./...", ".", nil, model.ClassVerify},
		{"secret redacted", "curl -H 'Authorization: Bearer abc.def' https://x", root, "curl -H 'Authorization: <redacted>' https://x", ".", nil, model.ClassVerify},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, ok := Normalize(c.cmd, c.cwd, home, root)
			if !ok {
				t.Fatal("not in repo")
			}
			if n.Command != c.want || n.Cwd != c.wantCwd {
				t.Errorf("got %q in %q, want %q in %q", n.Command, n.Cwd, c.want, c.wantCwd)
			}
			if strings.Join(n.Env, ",") != strings.Join(c.env, ",") {
				t.Errorf("env %v, want %v", n.Env, c.env)
			}
			if n.Class != c.class {
				t.Errorf("class %s, want %s", n.Class, c.class)
			}
		})
	}
	if _, ok := Normalize("ls", "/elsewhere", home, root); ok {
		t.Error("cwd outside the repo matched")
	}
	if _, ok := Normalize("ls", "/home/dev/src/app-other", home, root); ok {
		t.Error("sibling dir with a shared prefix matched")
	}
}

// Spec test 5: home and git toplevel collapse to the same fingerprint.
func TestSpec05_SameFingerprint(t *testing.T) {
	a, _ := Normalize("go test "+root+"/internal/...", root, home, root)
	b, _ := Normalize("go test ./internal/...", root, home, root)
	c, _ := Normalize("go test ./internal/...", root, "/Users/someone", root)
	if a.ID != b.ID || b.ID != c.ID {
		t.Fatalf("fingerprints differ: %s %s %s", a.ID, b.ID, c.ID)
	}
	other := "/Users/someone/work/app"
	d, _ := Normalize("go test "+other+"/internal/...", other, "/Users/someone", other)
	if d.ID != a.ID {
		t.Fatalf("same command on another machine got %s, want %s", d.ID, a.ID)
	}
	if len(a.ID) != 12 {
		t.Fatalf("fingerprint length %d", len(a.ID))
	}
}

func TestAnalyze(t *testing.T) {
	cases := []struct {
		cmd                              string
		denied, long, unreliable, verify bool
	}{
		{"ls -la", true, false, false, false},
		{"git status", true, false, false, false},
		{"git commit -m x", false, false, false, false},
		{"npm test", false, false, false, true},
		{"npm run test:unit", false, false, false, true},
		{"npm install", false, false, false, false},
		{"npm run dev", false, true, false, false},
		{"pnpm --filter api test", false, false, false, true},
		{"cargo test -p api", false, false, false, true},
		{"cargo watch -x test", false, true, false, false},
		{"go test ./...", false, false, false, true},
		{"go run ./cmd/server", false, true, false, false},
		{"pytest -k auth", false, false, false, true},
		{"python -m pytest tests/", false, false, false, true},
		{"python manage.py migrate", false, false, false, true},
		{"python script.py", false, false, false, false},
		{"make test", false, false, false, true},
		{"make", false, false, false, false},
		{"./gradlew test", false, false, false, true},
		{"npx vitest run", false, false, false, true},
		{"npx vite", false, true, false, false},
		{"next dev", false, true, false, false},
		{"vite build", false, false, false, false},
		{"npm test 2>&1 | tail -20", false, false, true, true},
		{"npm test; echo done", false, false, true, true},
		{"npm run build && npm test", false, false, false, true},
		{"npm start &", false, true, false, false},
		{"uv run pytest", false, false, false, true},
		{"bundle exec rspec", false, false, false, true},
		{"npm test 2>&1", false, false, false, true},
	}
	for _, c := range cases {
		sh := Analyze(c.cmd)
		if sh.Denied != c.denied || sh.LongRunning != c.long || sh.Unreliable != c.unreliable || sh.ProjectVerb != c.verify {
			t.Errorf("%q: got %+v", c.cmd, sh)
		}
	}
}

func TestRedact(t *testing.T) {
	cases := []struct{ in, mustNot string }{
		{"Authorization: Bearer sk-live-123", "sk-live-123"},
		{"curl -H 'Bearer abcdef123'", "abcdef123"},
		{"https://x?token=abc123&y=1", "abc123"},
		{"api_key=zzz111", "zzz111"},
		{"GITHUB_TOKEN=ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ghp_aaaa"},
		{"github_pat_11ABCDEFG0123456789_abcdefghij", "github_pat_11"},
		{"xoxb-1234567890-abcdefghij", "xoxb-1234567890"},
		{"AKIAIOSFODNN7EXAMPLE leaked", "AKIAIOSFODNN7EXAMPLE"},
		{"postgres://admin:hunter2@db:5432/app", "hunter2"},
		{"redis://:s3cret@cache:6379", "s3cret"},
		{"-----BEGIN RSA PRIVATE KEY----- MIIE", "MIIE"},
		{"password: \"p@ss word\"", "p@ss"},
	}
	for _, c := range cases {
		if got := Redact(c.in); strings.Contains(got, c.mustNot) {
			t.Errorf("Redact(%q) = %q still contains %q", c.in, got, c.mustNot)
		}
	}
	if got := Redact("Authorization: Bearer x"); !strings.Contains(got, "Authorization:") {
		t.Errorf("key dropped: %q", got)
	}
}

func TestSensitive(t *testing.T) {
	for _, c := range []string{"cat .env", "source .env.local", "dotenv -e .env.test -- npm test", "cat ~/.ssh/id_rsa", "openssl x509 -in cert.pem"} {
		if !SensitiveCommand(c) {
			t.Errorf("%q not flagged", c)
		}
	}
	for _, c := range []string{"npm test", "cat environment.md", "go test ./envconfig"} {
		if SensitiveCommand(c) {
			t.Errorf("%q flagged", c)
		}
	}
}

func TestHead(t *testing.T) {
	if got := Head("\n\n\x1b[31mError: boom\x1b[0m\nmore"); got != "Error: boom" {
		t.Errorf("got %q", got)
	}
	if got := Head(strings.Repeat("あ", 300)); len([]rune(got)) != 200 {
		t.Errorf("not capped: %d", len([]rune(got)))
	}
}

func TestKeep(t *testing.T) {
	cases := []struct {
		name   string
		events []model.Event
		check  func(t *testing.T, obs []model.Observation, res []model.Resolved)
	}{
		{
			// Spec test 1.
			name: "failure then success with a shared path",
			events: []model.Event{
				func() model.Event {
					e := shell("claude", "s1", "npx vitest run src/auth.test.ts", one, 0)
					e.StderrHead = "Cannot find module '@/lib/auth'"
					return e
				}(),
				edit("claude", "s1", root+"/src/auth.ts", time.Minute),
				shell("claude", "s1", "npx vitest run src/auth.test.ts", zero, 2*time.Minute),
			},
			check: func(t *testing.T, obs []model.Observation, _ []model.Resolved) {
				ok := successes(obs)
				if len(ok) != 1 || !ok[0].Paired || !ok[0].Eligible {
					t.Fatalf("want one paired success, got %+v", obs)
				}
				if strings.Join(ok[0].After, ",") != "src/auth.ts" || ok[0].Was != "Cannot find module '@/lib/auth'" {
					t.Fatalf("after=%v was=%q", ok[0].After, ok[0].Was)
				}
				if !obs[0].Consumed {
					t.Fatal("failure not consumed")
				}
			},
		},
		{
			name: "different command sharing a token pairs",
			events: []model.Event{
				shell("codex", "s", "go test ./internal/auth -run TestLogin", one, 0),
				shell("codex", "s", "go test ./internal/auth", zero, time.Minute),
			},
			check: func(t *testing.T, obs []model.Observation, _ []model.Resolved) {
				if ok := successes(obs); len(ok) != 1 || !ok[0].Paired {
					t.Fatalf("not paired: %+v", obs)
				}
			},
		},
		{
			name: "outside the window does not pair",
			events: []model.Event{
				shell("codex", "s", "go run ./tools/gen api", one, 0),
				shell("codex", "s", "go run ./tools/gen api", zero, 31*time.Minute),
			},
			check: func(t *testing.T, obs []model.Observation, _ []model.Resolved) {
				if ok := successes(obs); len(ok) != 0 {
					t.Fatalf("long-running go run kept: %+v", ok)
				}
			},
		},
		{
			name: "non project verb success after unrelated failure is not eligible",
			events: []model.Event{
				shell("codex", "s", "node seed.js", one, 0),
				shell("codex", "s", "node report.js", zero, time.Minute),
			},
			check: func(t *testing.T, obs []model.Observation, _ []model.Resolved) {
				if ok := successes(obs); len(ok) != 1 || ok[0].Eligible {
					t.Fatalf("got %+v", ok)
				}
			},
		},
		{
			// Spec test 2.
			name:   "ls exiting 0 is dropped",
			events: []model.Event{shell("claude", "s", "ls -la", zero, 0)},
			check:  wantNone,
		},
		{
			// Spec test 3.
			name:   "bare npm test is kept",
			events: []model.Event{shell("claude", "s", "npm test", zero, 0)},
			check: func(t *testing.T, obs []model.Observation, _ []model.Resolved) {
				if len(obs) != 1 || !obs[0].Eligible || obs[0].Paired {
					t.Fatalf("got %+v", obs)
				}
			},
		},
		{
			// Spec test 7.
			name:   ".env read is ignored",
			events: []model.Event{shell("claude", "s", "cat .env && npm test", zero, 0)},
			check:  wantNone,
		},
		{
			// Spec test 13.
			name:   "nil exit creates nothing",
			events: []model.Event{shell("claude", "s", "npm test", nil, 0)},
			check:  wantNone,
		},
		{
			name: "pipeline exit is not trusted",
			events: []model.Event{
				shell("claude", "s", "npm test 2>&1 | tail -5", zero, 0),
			},
			check: wantNone,
		},
		{
			name: "edit to .env is not attached",
			events: []model.Event{
				shell("claude", "s", "npm test", one, 0),
				edit("claude", "s", root+"/.env", time.Second),
				edit("claude", "s", root+"/src/a.ts", 2*time.Second),
				shell("claude", "s", "npm test", zero, 3*time.Second),
			},
			check: func(t *testing.T, obs []model.Observation, _ []model.Resolved) {
				ok := successes(obs)
				if len(ok) != 1 || strings.Join(ok[0].After, ",") != "src/a.ts" {
					t.Fatalf("got %+v", ok)
				}
			},
		},
		{
			name: "sessions are independent",
			events: []model.Event{
				shell("claude", "a", "npm test", one, 0),
				shell("claude", "b", "npm test", zero, time.Minute),
			},
			check: func(t *testing.T, obs []model.Observation, _ []model.Resolved) {
				ok := successes(obs)
				if len(ok) != 1 || ok[0].Paired || obs[0].Consumed {
					t.Fatalf("paired across sessions: %+v", obs)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obs, res := Keep(c.events, nil, home, root)
			c.check(t, obs, res)
		})
	}
}

func TestKeepSeenResolvesAcrossSyncs(t *testing.T) {
	events := []model.Event{
		shell("claude", "s", "npm test", one, 0),
		shell("claude", "s", "npm test", zero, time.Minute),
	}
	obs, res := Keep(events, []bool{true, false}, home, root)
	if len(obs) != 1 || !obs[0].Paired {
		t.Fatalf("got %+v", obs)
	}
	if len(res) != 1 || res[0].ID != obs[0].ID {
		t.Fatalf("resolved %+v", res)
	}
	obs, _ = Keep(events, []bool{true, true}, home, root)
	if len(obs) != 0 {
		t.Fatalf("seen events counted again: %+v", obs)
	}
}

func successes(obs []model.Observation) []model.Observation {
	var out []model.Observation
	for _, o := range obs {
		if o.OK {
			out = append(out, o)
		}
	}
	return out
}

func wantNone(t *testing.T, obs []model.Observation, _ []model.Resolved) {
	t.Helper()
	if len(successes(obs)) != 0 {
		t.Fatalf("want nothing kept, got %+v", obs)
	}
}

// Spec test 6: a bearer token in stderr does not appear in `was`, and the
// text around it survives.
func TestSpec06_BearerNotInWas(t *testing.T) {
	e := shell("claude", "s", "npm test", one, 0)
	e.StderrHead = "request failed (Authorization: Bearer abc.DEF-123) retry"
	obs, _ := Keep([]model.Event{e, shell("claude", "s", "npm test", zero, time.Minute)}, nil, home, root)
	ok := successes(obs)
	if len(ok) != 1 || strings.Contains(ok[0].Was, "abc.DEF-123") {
		t.Fatalf("got %+v", ok)
	}
	if ok[0].Was != "request failed (Authorization: <redacted>) retry" {
		t.Fatalf("was = %q", ok[0].Was)
	}
}
