package scan

import "testing"

func TestCline(t *testing.T) {
	l := newLayout(t)
	const id, other = "1760000000000_abc12", "1760000000001_def34"
	l.place(t, "cline/session.messages.json", ".cline/data/sessions/"+id+"/"+id+".messages.json")
	l.place(t, "cline/manifest.json", ".cline/data/sessions/"+id+"/"+id+".json")
	l.place(t, "cline/elsewhere.messages.json", ".cline/data/sessions/"+other+"/"+other+".messages.json")
	l.place(t, "cline/elsewhere.manifest.json", ".cline/data/sessions/"+other+"/"+other+".json")
	st, events, _ := collect(t, Cline{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	// Cline writes an exit code only for a non-zero exit; a success has none.
	checkContract(t, "cline", l, events, contract{paired: ""})

	var failed, edit bool
	for _, e := range events {
		switch {
		case e.Command == "npm test" && e.Exit != nil:
			if *e.Exit != 1 {
				t.Errorf("npm test exit = %d", *e.Exit)
			}
			failed = true
		case e.Kind == "shell" && e.Exit != nil:
			t.Errorf("exit inferred without a marker: %+v", e)
		case e.Kind == "edit" && e.Path == l.Root+"/src/missing.ts":
			t.Errorf("failed edit kept: %+v", e)
		case e.Kind == "edit" && e.Path == l.Root+"/src/auth.ts":
			edit = true
		}
		if e.Cwd != l.Root {
			t.Errorf("cwd = %q", e.Cwd)
		}
	}
	if !failed || !edit {
		t.Errorf("failed=%v edit=%v\n%s", failed, edit, dump(events))
	}
}

func TestClineSessionDataDirOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "cline/session.messages.json", "data/s1/s1.messages.json")
	l.place(t, "cline/manifest.json", "data/s1/s1.json")
	l.place(t, "cline/session.messages.json", ".cline/data/sessions/s2/s2.messages.json")
	_, srcs := Cline{}.Discover(l.Home, env(map[string]string{"CLINE_SESSION_DATA_DIR": l.Home + "/data"}))
	if len(srcs) != 1 {
		t.Fatalf("got %v", srcs)
	}
}
