package scan

import "testing"

func TestGrok(t *testing.T) {
	l := newLayout(t)
	const sid, other = "019a0000-0000-7000-8000-000000000001", "019a0000-0000-7000-8000-000000000002"
	l.place(t, "grok/updates.jsonl", ".grok/sessions/%2Fwork%2Fapp/"+sid+"/updates.jsonl")
	l.place(t, "grok/summary.json", ".grok/sessions/%2Fwork%2Fapp/"+sid+"/summary.json")
	l.place(t, "grok/elsewhere.updates.jsonl", ".grok/sessions/%2Fwork%2Fother/"+other+"/updates.jsonl")
	l.place(t, "grok/elsewhere.summary.json", ".grok/sessions/%2Fwork%2Fother/"+other+"/summary.json")
	st, events, _ := collect(t, Grok{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	checkContract(t, "grok", l, events, contract{paired: "npm test", after: "src/auth.ts"})
	for _, e := range events {
		if (e.Command == unknownExit || e.Command == "npm run dev") && e.Exit != nil {
			t.Errorf("exit from a non-final or killed run: %+v", e)
		}
		if e.Kind == "edit" && e.Path == l.Root+"/src/missing.ts" {
			t.Errorf("failed edit kept: %+v", e)
		}
	}
}

func TestGrokHomeOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "grok/updates.jsonl", "gh/sessions/x/s/updates.jsonl")
	l.place(t, "grok/summary.json", "gh/sessions/x/s/summary.json")
	l.place(t, "grok/elsewhere.updates.jsonl", ".grok/sessions/y/s/updates.jsonl")
	_, srcs := Grok{}.Discover(l.Home, env(map[string]string{"GROK_HOME": l.Home + "/gh"}))
	if len(srcs) != 1 {
		t.Fatalf("got %v", srcs)
	}
}
