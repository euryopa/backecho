package scan

import "testing"

func TestDroid(t *testing.T) {
	l := newLayout(t)
	l.place(t, "droid/session.jsonl", ".factory/sessions/-work-app/8a7b6c5d-1111-4222-8333-444455556666.jsonl")
	l.place(t, "droid/settings.json", ".factory/sessions/-work-app/8a7b6c5d-1111-4222-8333-444455556666.settings.json")
	l.place(t, "droid/elsewhere.jsonl", ".factory/sessions/-work-other/8a7b6c5d-9999-4222-8333-444455556666.jsonl")
	st, events, _ := collect(t, Droid{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	checkContract(t, "droid", l, events, contract{paired: "npm test", after: "src/auth.ts"})
	for _, e := range events {
		if e.Command == unknownExit && e.Exit != nil {
			t.Errorf("exit read from result text: %+v", e)
		}
		if e.Kind == "edit" && e.Path == l.Root+"/src/broken.ts" {
			t.Errorf("errored edit kept: %+v", e)
		}
	}
}

func TestDroidSessionsDirOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "droid/session.jsonl", "ds/p/s.jsonl")
	l.place(t, "droid/settings.json", "ds/p/s.settings.json")
	l.place(t, "droid/elsewhere.jsonl", ".factory/sessions/p/ignored.jsonl")
	_, srcs := Droid{}.Discover(l.Home, env(map[string]string{"DROID_SESSIONS_DIR": l.Home + "/ds"}))
	if len(srcs) != 1 {
		t.Fatalf("got %v", srcs)
	}
}
