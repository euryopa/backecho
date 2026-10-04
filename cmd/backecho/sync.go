package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
	"github.com/euryopa/backecho/internal/render"
	"github.com/euryopa/backecho/internal/scan"
	"github.com/euryopa/backecho/internal/store"
)

// selectAdapters returns all adapters, or the comma-separated subset.
func selectAdapters(list string) ([]scan.Adapter, error) {
	all := scan.All()
	if strings.TrimSpace(list) == "" {
		return all, nil
	}
	byID := map[string]scan.Adapter{}
	for _, a := range all {
		byID[a.ID()] = a
	}
	var out []scan.Adapter
	for _, id := range strings.Split(list, ",") {
		id = strings.TrimSpace(strings.ToLower(id))
		if id == "" {
			continue
		}
		a, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown agent %q (known: %s)", id, strings.Join(scan.IDs(), ", "))
		}
		out = append(out, a)
	}
	return out, nil
}

// sources discovers an adapter's global and repo-local sources.
func (a *app) sources(ad scan.Adapter, root string) (scan.Status, []scan.Source) {
	st, srcs := ad.Discover(a.home, a.env)
	if ra, ok := ad.(scan.RepoAdapter); ok {
		if local := ra.DiscoverRepo(root); len(local) > 0 {
			srcs = append(srcs, local...)
			if st.State != "found" {
				st.State = "found"
				st.Detail = ""
			}
			st.Detail = strings.TrimSpace(strings.TrimPrefix(st.Detail+"; repo-local", "; "))
		}
	}
	return st, srcs
}

// statSource returns the size and mtime that decide whether a handle changed.
// A SQLite database counts its write-ahead log too.
func statSource(src scan.Source) (int64, time.Time, error) {
	info, err := os.Stat(src.Path)
	if err != nil {
		return 0, time.Time{}, err
	}
	size, mtime := info.Size(), info.ModTime().UTC()
	if src.Kind == "sqlite" {
		if wal, err := os.Stat(src.Path + "-wal"); err == nil {
			size += wal.Size()
			if wal.ModTime().After(mtime) {
				mtime = wal.ModTime().UTC()
			}
		}
	}
	return size, mtime, nil
}

// adapterRun is what one adapter produced during a scan.
type adapterRun struct {
	status       model.AdapterStatus
	events       []model.Event
	seen         []bool
	unrecognized int
	failed       int
	changed      int
}

// scanAdapter discovers and parses one adapter. With st non-nil, unchanged
// handles are skipped and the cursor is updated; with st nil every source is
// parsed (status).
func (a *app) scanAdapter(ad scan.Adapter, root string, since time.Time, st *store.State) adapterRun {
	ds, srcs := a.sources(ad, root)
	r := adapterRun{status: model.AdapterStatus{Agent: ad.ID(), State: ds.State, Detail: ds.Detail, Sources: len(srcs)}}
	var errs []string
	for _, src := range srcs {
		size, mtime, err := statSource(src)
		if err != nil {
			r.failed++
			errs = append(errs, err.Error())
			continue
		}
		plan := store.Plan{}
		if st != nil {
			plan = st.PlanFor(ad.ID(), src.Path, size, mtime)
			if plan.Skip {
				continue
			}
		}
		// Parse without --since so the cursor counts stay stable; --since
		// is applied below by marking older events as already seen.
		events, err := ad.Parse(src, root, time.Time{})
		if err != nil {
			if errors.Is(err, scan.ErrUnrecognized) {
				r.unrecognized++
			} else {
				r.failed++
			}
			errs = append(errs, filepath.Base(src.Path)+": "+err.Error())
			continue
		}
		r.changed++
		for i := range events {
			if events[i].TS.IsZero() {
				events[i].TS = mtime
			}
			if events[i].Agent == "" {
				events[i].Agent = ad.ID()
			}
			old := i < plan.Seen || (!since.IsZero() && events[i].TS.Before(since))
			r.events = append(r.events, events[i])
			r.seen = append(r.seen, old)
		}
		r.status.Events += len(events)
		if st != nil {
			st.Record(ad.ID(), src.Path, src.Kind, size, mtime, len(events))
		}
	}

	bad := r.unrecognized + r.failed
	switch {
	case len(srcs) > 0 && bad == len(srcs):
		r.status.State = "skipped"
		r.status.Detail = fmt.Sprintf("%d of %d sources unreadable or unrecognized: %s", bad, len(srcs), errs[0])
	case bad > 0:
		r.status.Detail = fmt.Sprintf("%s; %d unrecognized, %d unreadable (first: %s)", ds.Detail, r.unrecognized, r.failed, errs[0])
	}
	return r
}

func (a *app) sync(args []string) int {
	fs := a.flags("sync")
	agents := fs.String("agent", "", "comma-separated adapter ids (default: all detected)")
	dryRun := fs.Bool("dry-run", false, "print the rendered verify.md to stdout and write nothing")
	sinceFlag := fs.String("since", "", "only consider records on or after this date (YYYY-MM-DD)")
	dirFlag := fs.String("dir", "", "data directory (default .backecho at the git toplevel)")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	var since time.Time
	if *sinceFlag != "" {
		t, err := time.Parse("2006-01-02", *sinceFlag)
		if err != nil {
			a.logf("--since: want YYYY-MM-DD, got %q", *sinceFlag)
			return exitUsage
		}
		since = t
	}
	ads, err := selectAdapters(*agents)
	if err != nil {
		a.logf("%v", err)
		return exitUsage
	}
	root, dir, code, ok := a.repo(*dirFlag)
	if !ok {
		return code
	}

	state, err := store.LoadState(dir)
	if err != nil {
		a.logf("%v", err)
		return exitError
	}
	st, err := store.Load(dir)
	if err != nil {
		a.logf("%v", err)
		return exitError
	}

	var events []model.Event
	var seen []bool
	for _, ad := range ads {
		r := a.scanAdapter(ad, root, since, state)
		events = append(events, r.events...)
		seen = append(seen, r.seen...)
		switch r.status.State {
		case "found":
			fresh := 0
			for _, s := range r.seen {
				if !s {
					fresh++
				}
			}
			a.logf("%-12s %d sources, %d changed, %d new repo events%s", ad.ID(), r.status.Sources, r.changed, fresh, detailSuffix(r))
		case "skipped":
			a.logf("%-12s skipped: %s", ad.ID(), r.status.Detail)
		}
	}

	obs, resolved := norm.Keep(events, seen, a.home, root)
	res := store.Merge(st, obs, resolved)
	removed := store.Age(st, a.now)

	verifyMD, localMD := render.Verify(st), render.Local(st)
	if *dryRun {
		a.stdout.Write(verifyMD)
		a.logf("dry run: %d new, %d updated, %d open, %d removed; nothing written", res.Inserted, res.Updated, res.Open, removed)
		return exitOK
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.logf("%v", err)
		return exitError
	}
	a.ensureGitignore(root, dir, state)
	if err := store.Save(dir, st); err != nil {
		a.logf("%v", err)
		return exitError
	}
	for name, body := range map[string][]byte{"verify.md": verifyMD, "local.md": localMD} {
		if err := store.WriteFileAtomic(filepath.Join(dir, name), body); err != nil {
			a.logf("%v", err)
			return exitError
		}
	}
	if err := store.SaveState(dir, state); err != nil {
		a.logf("%v", err)
		return exitError
	}
	if res.Inserted+res.Updated+res.Open+removed == 0 {
		a.logf("nothing new; %s is up to date", relTo(a.cwd, filepath.Join(dir, "verify.md")))
	} else {
		a.logf("%d new, %d updated, %d open, %d removed -> %s", res.Inserted, res.Updated, res.Open, removed, relTo(a.cwd, filepath.Join(dir, "verify.md")))
	}
	return exitOK
}

func detailSuffix(r adapterRun) string {
	if r.unrecognized+r.failed == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d unrecognized, %d unreadable)", r.unrecognized, r.failed)
}

// ensureGitignore appends the machine-specific files once. Without a
// .gitignore it warns once and creates nothing.
func (a *app) ensureGitignore(root, dir string, state *store.State) {
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return
	}
	added, missing, err := store.EnsureGitignore(root, store.IgnoreLines(rel))
	switch {
	case err != nil:
		a.logf("could not update .gitignore: %v", err)
	case missing && !state.GitignoreWarned:
		a.logf("no .gitignore in this repo; add %s yourself so machine-specific notes stay local", strings.Join(store.IgnoreLines(rel), ", "))
		state.GitignoreWarned = true
	case len(added) > 0:
		a.logf("added %s to .gitignore", strings.Join(added, ", "))
	}
}

// sortedStatuses keeps status output in All() order.
func sortedStatuses(rs []model.AdapterStatus, order []string) {
	idx := map[string]int{}
	for i, id := range order {
		idx[id] = i
	}
	sort.SliceStable(rs, func(i, j int) bool { return idx[rs[i].Agent] < idx[rs[j].Agent] })
}
