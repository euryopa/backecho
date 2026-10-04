package scan

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

func TestKimi(t *testing.T) {
	l := newLayout(t)
	l.place(t, "kimi/wire.jsonl", ".kimi-code/sessions/wd_app_0123456789ab/ses_main/agents/main/wire.jsonl")
	l.place(t, "kimi/elsewhere.jsonl", ".kimi-code/sessions/wd_other_0123456789ab/ses_other/agents/main/wire.jsonl")
	st, events, _ := collect(t, Kimi{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	for _, e := range events {
		if e.Kind == model.KindEdit && strings.Contains(e.Path, "denied.go") {
			t.Errorf("failed edit kept: %+v", e)
		}
		if e.Kind == model.KindShell && e.Command == unknownExit && e.Exit != nil {
			t.Errorf("success with output got an exit: %+v", e)
		}
	}
	checkContract(t, "kimi", l, events, contract{paired: "go vet ./...", after: "auth/auth.go"})
}

// A wire with no cwd record takes the work dir from session_index.jsonl.
func TestKimiSessionIndexCwd(t *testing.T) {
	l := newLayout(t)
	p := l.place(t, "kimi/elsewhere.jsonl", ".kimi-code/sessions/wd_app/ses_x/agents/main/wire.jsonl")
	b, _ := os.ReadFile(p)
	b = []byte(strings.Replace(string(b), `"cwd":"`+jsonEscape(filepath.Join(filepath.Dir(l.Root), "other"))+`",`, "", 1))
	os.WriteFile(p, b, 0o644)
	idx := `{"sessionId":"ses_x","sessionDir":"` + jsonEscape(filepath.Dir(filepath.Dir(filepath.Dir(p)))) + `","workDir":"` + jsonEscape(l.Root) + `"}` + "\n"
	os.WriteFile(filepath.Join(l.Home, ".kimi-code", "session_index.jsonl"), []byte(idx), 0o644)
	events, err := Kimi{}.Parse(Source{Agent: "kimi", Path: p}, l.Root, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Cwd != l.Root || events[0].Exit == nil || *events[0].Exit != 0 {
		t.Fatalf("got\n%s", dump(events))
	}
}

// Legacy Kimi CLI (Python): ~/.kimi/sessions/<md5(workDir)>/<id>/context.jsonl.
func TestKimiLegacyContext(t *testing.T) {
	l := newLayout(t)
	sum := md5.Sum([]byte(l.Root))
	bucket := hex.EncodeToString(sum[:])
	p := l.place(t, "kimi/context.jsonl", ".kimi/sessions/"+bucket+"/3f1e/context.jsonl")
	os.WriteFile(filepath.Join(l.Home, ".kimi", "kimi.json"), []byte(`{"work_dirs":[{"path":"`+jsonEscape(l.Root)+`","kaos":"local","last_session_id":"3f1e"}]}`), 0o644)
	st, srcs := Kimi{}.Discover(l.Home, env(nil))
	if st.State != "found" || len(srcs) != 1 || srcs[0].Path != p {
		t.Fatalf("discover %+v %v", st, srcs)
	}
	events, err := Kimi{}.Parse(srcs[0], l.Root, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		exit := "nil"
		if e.Exit != nil {
			exit = string(rune('0' + *e.Exit))
		}
		if e.Cwd != l.Root {
			t.Errorf("cwd %q", e.Cwd)
		}
		got = append(got, string(e.Kind)+":"+e.Command+e.Path+":"+exit)
	}
	want := "shell:npm test:1,edit:" + l.Root + "/src/auth.ts:nil,shell:npm test:0,shell:make test-unknown:nil"
	if strings.Join(got, ",") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, ","), want)
	}
}

func TestKimiEnvRoot(t *testing.T) {
	l := newLayout(t)
	l.place(t, "kimi/wire.jsonl", "kc/sessions/k/s/agents/main/wire.jsonl")
	l.place(t, "kimi/wire.jsonl", ".kimi-code/sessions/k/s/agents/main/wire.jsonl")
	_, srcs := Kimi{}.Discover(l.Home, env(map[string]string{"KIMI_CODE_HOME": l.Home + "/kc"}))
	if len(srcs) != 1 || !strings.Contains(srcs[0].Path, "/kc/") {
		t.Fatalf("got %v", srcs)
	}
}
