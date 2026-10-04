package scan

import (
	"strings"
	"testing"
)

func TestCopilot(t *testing.T) {
	l := newLayout(t)
	l.place(t, "copilot/events.jsonl", ".copilot/session-state/79a67e6e-fa73-4bbb-b40b-f5c782ddca51/events.jsonl")
	l.place(t, "copilot/elsewhere.jsonl", ".copilot/session-state/0b1c2d3e-0000-4bbb-b40b-f5c782ddca52/events.jsonl")
	st, events, _ := collect(t, Copilot{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	checkContract(t, "copilot", l, events, contract{paired: "npm test", after: "src/auth.ts"})

	var vet bool
	for _, e := range events {
		switch {
		case e.Command == "go vet ./..." && e.Exit != nil && *e.Exit == 0:
			vet = true
		case e.Command == "npm run lint" && e.Exit != nil:
			t.Errorf("success:false read as an exit status: %+v", e)
		case e.Command == unknownExit && e.Exit != nil:
			t.Errorf("unknown exit got a status: %+v", e)
		case strings.HasSuffix(e.Path, "refused.ts"):
			t.Errorf("failed edit kept: %+v", e)
		}
	}
	if !vet {
		t.Error("terminal contents exitCode not read")
	}
}

func TestCopilotMarker(t *testing.T) {
	cases := map[string]int{
		"out\n<exited with exit code 2>\n":                  2,
		"<shellId: 0 completed with exit code 0>":           0,
		"line\n<shellId: abc completed with exit code 127>": 127,
	}
	for in, want := range cases {
		if _, got := copilotSplitMarker(in); got == nil || *got != want {
			t.Errorf("%q: got %v want %d", in, got, want)
		}
	}
	if _, got := copilotSplitMarker("echo '<exited with exit code 0>' then more"); got != nil {
		t.Errorf("marker not on its own last line accepted")
	}
}

func TestCopilotHomeOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "copilot/events.jsonl", "cp/session-state/s1/events.jsonl")
	l.place(t, "copilot/elsewhere.jsonl", ".copilot/session-state/s2/events.jsonl")
	_, srcs := Copilot{}.Discover(l.Home, env(map[string]string{"COPILOT_HOME": l.Home + "/cp"}))
	if len(srcs) != 1 {
		t.Fatalf("COPILOT_HOME should replace the default: %v", srcs)
	}
}
