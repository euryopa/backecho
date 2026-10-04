package scan

import (
	"path/filepath"
	"testing"
)

func TestPi(t *testing.T) {
	l := newLayout(t)
	l.place(t, "pi/session.jsonl", ".pi/agent/sessions/--work-app--/2026-10-04T14-00-00-000Z_a1b2c3d4.jsonl")
	l.place(t, "pi/elsewhere.jsonl", ".pi/agent/sessions/--work-other--/2026-10-04T15-00-00-000Z_a1b2c3d4.jsonl")
	l.place(t, "pi/omp.jsonl", ".omp/agent/sessions/--work-app--/2026-10-04T16-00-00-000Z_omp.jsonl")
	st, events, _ := collect(t, Pi{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	checkContract(t, "pi", l, events, contract{paired: "npm test", after: "src/auth.ts"})

	var vet, cargo bool
	for _, e := range events {
		switch e.Command {
		case "go vet ./...":
			vet = e.Exit != nil && *e.Exit == 0
		case "cargo build":
			cargo = e.Exit != nil && *e.Exit == 101 && e.Cwd == filepath.Join(l.Root, "crates", "api")
		case "make test-unknown", "npm run dev", "npm run lint":
			if e.Exit != nil {
				t.Errorf("unknown exit got a status: %+v", e)
			}
		}
		if e.Path == "src/missing.ts" {
			t.Errorf("failed edit emitted: %+v", e)
		}
	}
	if !vet {
		t.Error("bashExecution exitCode not read")
	}
	if !cargo {
		t.Errorf("omp details.exitCode / relative cwd not read:\n%s", dump(events))
	}
}

func TestPiAgentDirOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "pi/session.jsonl", "custom/sessions/x/s.jsonl")
	l.place(t, "pi/elsewhere.jsonl", ".pi/agent/sessions/x/ignored.jsonl")
	l.place(t, "pi/omp.jsonl", ".omp/agent/sessions/x/ignored.jsonl")
	for _, key := range []string{"PI_AGENT_DIR", "PI_CODING_AGENT_DIR"} {
		_, srcs := Pi{}.Discover(l.Home, env(map[string]string{key: filepath.Join(l.Home, "custom")}))
		if len(srcs) != 1 {
			t.Fatalf("%s should replace both defaults: %v", key, srcs)
		}
	}
}
