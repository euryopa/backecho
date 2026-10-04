package scan

import (
	"path/filepath"
	"testing"
)

func TestAmp(t *testing.T) {
	l := newLayout(t)
	l.place(t, "amp/T-thread-0001.json", ".local/share/amp/threads/T-7c1e5f2a-0001.json")
	l.place(t, "amp/T-thread-0002.json", ".local/share/amp/threads/T-7c1e5f2a-0002.json")
	l.place(t, "amp/not-a-thread.json", ".local/share/amp/threads/settings.json")
	st, events, bad := collect(t, Amp{}, l, env(nil))
	if st.State != "found" {
		t.Fatalf("status %+v", st)
	}
	if bad != 1 {
		t.Errorf("non-thread JSON should be unrecognized, got %d", bad)
	}
	checkContract(t, "amp", l, events, contract{paired: "go test ./...", after: "internal/auth/auth.go"})
	for _, e := range events {
		if (e.Command == "make test-unknown" || e.Command == "npm run build") && e.Exit != nil {
			t.Errorf("unknown exit got a status: %+v", e)
		}
		if filepath.Base(e.Path) == "missing.go" {
			t.Errorf("failed edit emitted: %+v", e)
		}
	}
}

func TestAmpDataDirOverride(t *testing.T) {
	l := newLayout(t)
	l.place(t, "amp/T-thread-0001.json", "ampdata/threads/T-1.json")
	l.place(t, "amp/T-thread-0002.json", ".local/share/amp/threads/T-2.json")
	_, srcs := Amp{}.Discover(l.Home, env(map[string]string{"AMP_DATA_DIR": filepath.Join(l.Home, "ampdata")}))
	if len(srcs) != 1 {
		t.Fatalf("AMP_DATA_DIR should replace the default: %v", srcs)
	}
}
