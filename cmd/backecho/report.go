package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/render"
	"github.com/euryopa/backecho/internal/scan"
	"github.com/euryopa/backecho/internal/store"
)

func (a *app) printJSON(v any) int {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		a.logf("%v", err)
		return exitError
	}
	return exitOK
}

func (a *app) loadStore(flagDir string) (*model.Store, int, bool) {
	_, dir, code, ok := a.repo(flagDir)
	if !ok {
		return nil, code, false
	}
	st, err := store.Load(dir)
	if err != nil {
		a.logf("%v", err)
		return nil, exitError, false
	}
	return st, 0, true
}

func (a *app) show(args []string) int {
	fs := a.flags("show")
	asJSON := fs.Bool("json", false, "print store entries as JSON")
	dirFlag := fs.String("dir", "", "data directory (default .backecho at the git toplevel)")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	st, code, ok := a.loadStore(*dirFlag)
	if !ok {
		return code
	}
	if *asJSON {
		if st.Entries == nil {
			st.Entries = []model.Entry{}
		}
		return a.printJSON(st.Entries)
	}
	a.stdout.Write(render.Verify(st))
	hasLocal := false
	for _, e := range st.Entries {
		hasLocal = hasLocal || e.Class == model.ClassLocal
	}
	for _, o := range st.Open {
		hasLocal = hasLocal || o.Class == model.ClassLocal
	}
	if hasLocal {
		fmt.Fprintln(a.stdout)
		a.stdout.Write(render.Local(st))
	}
	return exitOK
}

func (a *app) stale(args []string) int {
	fs := a.flags("stale")
	asJSON := fs.Bool("json", false, "print stale entries as JSON")
	dirFlag := fs.String("dir", "", "data directory (default .backecho at the git toplevel)")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	st, code, ok := a.loadStore(*dirFlag)
	if !ok {
		return code
	}
	stale := []model.Entry{}
	for _, e := range st.Entries {
		if e.StaleSince != nil || store.IsStale(e, a.now) {
			stale = append(stale, e)
		}
	}
	if *asJSON {
		return a.printJSON(stale)
	}
	if len(stale) == 0 {
		a.logf("no stale entries")
		return exitOK
	}
	for _, e := range stale {
		reason := "not confirmed in 30 days"
		if e.FailCount > e.OKCount {
			reason = "fails more than it passes"
		}
		removal := ""
		if e.StaleSince != nil {
			removal = "  removed after: " + e.StaleSince.Add(store.DeleteAfter).Format("2006-01-02")
		}
		fmt.Fprintf(a.stdout, "- `%s`\n  cwd: %s  ok: %d  fail: %d  last: %s  (%s)%s\n",
			e.Command, e.Cwd, e.OKCount, e.FailCount, e.LastOK.Format("2006-01-02"), reason, removal)
	}
	return exitOK
}

func (a *app) status(args []string) int {
	fs := a.flags("status")
	asJSON := fs.Bool("json", false, "print adapter results as JSON")
	agents := fs.String("agent", "", "comma-separated adapter ids (default: all)")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	ads, err := selectAdapters(*agents)
	if err != nil {
		a.logf("%v", err)
		return exitUsage
	}
	root, _, code, ok := a.repo("")
	if !ok {
		return code
	}
	var out []model.AdapterStatus
	for _, ad := range ads {
		r := a.scanAdapter(ad, root, timeZero, nil)
		out = append(out, r.status)
	}
	sortedStatuses(out, scan.IDs())
	if *asJSON {
		return a.printJSON(out)
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tSTATE\tSOURCES\tEVENTS\tDETAIL")
	for _, s := range out {
		sources, events := fmt.Sprint(s.Sources), fmt.Sprint(s.Events)
		if s.State == "empty" {
			sources, events = "-", "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Agent, s.State, sources, events, s.Detail)
	}
	tw.Flush()
	fmt.Fprintf(a.stdout, "\nEVENTS counts shell and edit events recorded in %s.\n", root)
	return exitOK
}

var timeZero time.Time
