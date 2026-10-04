// Package store merges observations into the store, ages entries out, keeps
// the parse cursor, and writes files atomically. It never reads the clock:
// callers pass now.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/euryopa/backecho/internal/model"
	"github.com/euryopa/backecho/internal/norm"
)

const (
	Version = 1

	// StaleAfter is how old last_ok may get before an entry is stale.
	StaleAfter = 30 * 24 * time.Hour
	// DeleteAfter is how long an entry stays stale before it is removed.
	DeleteAfter = 30 * 24 * time.Hour
	// OpenTTL is how long an open failure is shown.
	OpenTTL = 7 * 24 * time.Hour

	maxAfter       = 8
	maxSessionKeys = 64

	StoreFile = "store.json" // verify entries, safe to commit
	LocalFile = "local.json" // local entries, gitignored
	StateFile = "state.json" // parse cursor, gitignored
)

// Load reads store.json and local.json from dir. Missing files are an empty
// store.
func Load(dir string) (*model.Store, error) {
	st := &model.Store{Version: Version}
	for _, name := range []string{StoreFile, LocalFile} {
		var part model.Store
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &part); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if part.Version > Version {
			return nil, fmt.Errorf("%s: version %d is newer than this backecho (%d)", name, part.Version, Version)
		}
		st.Entries = append(st.Entries, part.Entries...)
		st.Open = append(st.Open, part.Open...)
	}
	return st, nil
}

// Split returns the verify half (store.json) and the local half (local.json).
func Split(st *model.Store) (verify, local *model.Store) {
	verify = &model.Store{Version: Version, Entries: []model.Entry{}}
	local = &model.Store{Version: Version, Entries: []model.Entry{}}
	for _, e := range st.Entries {
		if e.Class == model.ClassLocal {
			local.Entries = append(local.Entries, e)
		} else {
			verify.Entries = append(verify.Entries, e)
		}
	}
	for _, o := range st.Open {
		if o.Class == model.ClassLocal {
			local.Open = append(local.Open, o)
		} else {
			verify.Open = append(verify.Open, o)
		}
	}
	return verify, local
}

// Save writes store.json and local.json atomically.
func Save(dir string, st *model.Store) error {
	sortStore(st)
	verify, local := Split(st)
	for name, part := range map[string]*model.Store{StoreFile: verify, LocalFile: local} {
		b, err := Marshal(part)
		if err != nil {
			return err
		}
		if err := WriteFileAtomic(filepath.Join(dir, name), b); err != nil {
			return err
		}
	}
	return nil
}

// Marshal encodes a store the way it is written to disk.
func Marshal(st *model.Store) ([]byte, error) {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func sortStore(st *model.Store) {
	sort.SliceStable(st.Entries, func(i, j int) bool {
		a, b := st.Entries[i], st.Entries[j]
		if a.Command != b.Command {
			return a.Command < b.Command
		}
		return a.Cwd < b.Cwd
	})
	sort.SliceStable(st.Open, func(i, j int) bool { return st.Open[i].TS.Before(st.Open[j].TS) })
}

// Result counts what a merge changed.
type Result struct {
	Inserted int
	Updated  int
	Open     int
}

// Merge folds observations into the store. A new fingerprint inserts; an
// existing one increments counts, updates timestamps, replaces `was` only
// when the new failure is newer, and unions `after` and `agents`.
func Merge(st *model.Store, obs []model.Observation, resolved []model.Resolved) Result {
	var res Result
	index := map[string]int{}
	for i, e := range st.Entries {
		index[e.ID] = i
	}
	sorted := append([]model.Observation(nil), obs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TS.Before(sorted[j].TS) })

	touched := map[string]bool{}
	for _, o := range sorted {
		i, exists := index[o.ID]
		if o.OK {
			if !exists {
				if !o.Eligible {
					continue
				}
				st.Entries = append(st.Entries, model.Entry{
					ID: o.ID, Command: o.Command, Cwd: o.Cwd, Class: o.Class,
					LastOK: o.TS, Agents: []string{},
				})
				i = len(st.Entries) - 1
				index[o.ID] = i
				res.Inserted++
				touched[o.ID] = true
			} else if !touched[o.ID] {
				res.Updated++
				touched[o.ID] = true
			}
			e := &st.Entries[i]
			e.OKCount++
			if !o.TS.Before(e.LastOK) {
				e.LastOK = o.TS
				e.LastAgent = o.Agent
			}
			if e.LastAgent == "" {
				e.LastAgent = o.Agent
			}
			e.Agents = norm.Union(e.Agents, []string{o.Agent}, 0)
			// A run without env or machine paths proves the command is
			// portable, so the entry moves to verify and drops env names.
			if o.Class == model.ClassVerify {
				e.Class = model.ClassVerify
			}
			if e.Class == model.ClassLocal {
				e.Env = norm.Union(e.Env, o.Env, 0)
			} else {
				e.Env = nil
			}
			if !contains(e.SessionKeys, o.Session) {
				e.Sessions++
				e.SessionKeys = append(e.SessionKeys, o.Session)
				if len(e.SessionKeys) > maxSessionKeys {
					e.SessionKeys = e.SessionKeys[len(e.SessionKeys)-maxSessionKeys:]
				}
			}
			if o.Paired {
				e.FailCount++
				noteFailure(e, o.WasTS, o.Was)
				e.After = norm.Union(e.After, o.After, maxAfter)
			}
			continue
		}

		if o.Countable && !o.Consumed && exists {
			e := &st.Entries[i]
			e.FailCount++
			noteFailure(e, o.TS, o.Hint)
			if !touched[o.ID] {
				res.Updated++
				touched[o.ID] = true
			}
		}
		if !o.Consumed {
			addOpen(st, model.Open{
				ID: o.ID, Command: o.Command, Cwd: o.Cwd, Class: o.Class,
				Agent: o.Agent, Session: o.Session, TS: o.TS, Hint: o.Hint,
			})
			res.Open++
		}
	}

	if len(resolved) > 0 {
		kept := st.Open[:0]
		for _, op := range st.Open {
			drop := false
			for _, r := range resolved {
				if r.ID == op.ID && r.Session == op.Session && !op.TS.After(r.TS) {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, op)
			}
		}
		st.Open = kept
	}
	return res
}

func noteFailure(e *model.Entry, ts time.Time, hint string) {
	if e.LastFail == nil || ts.After(*e.LastFail) {
		t := ts
		e.LastFail = &t
		if hint != "" {
			e.Was = hint
		}
	} else if e.Was == "" && hint != "" {
		e.Was = hint
	}
}

func addOpen(st *model.Store, o model.Open) {
	for i, x := range st.Open {
		if x.ID == o.ID && x.Session == o.Session {
			if o.TS.After(x.TS) {
				st.Open[i] = o
			}
			return
		}
	}
	st.Open = append(st.Open, o)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Age marks entries stale or fresh, removes entries that have been stale for
// DeleteAfter, and expires open failures older than OpenTTL. It returns how
// many entries were removed.
func Age(st *model.Store, now time.Time) int {
	removed := 0
	kept := st.Entries[:0]
	for _, e := range st.Entries {
		if IsStale(e, now) {
			if e.StaleSince == nil {
				t := now
				e.StaleSince = &t
			}
			if now.Sub(*e.StaleSince) > DeleteAfter {
				removed++
				continue
			}
		} else {
			e.StaleSince = nil
		}
		kept = append(kept, e)
	}
	st.Entries = kept

	open := st.Open[:0]
	for _, o := range st.Open {
		if now.Sub(o.TS) <= OpenTTL {
			open = append(open, o)
		}
	}
	st.Open = open
	return removed
}

// IsStale reports whether last_ok is older than StaleAfter, or failures
// outnumber successes.
func IsStale(e model.Entry, now time.Time) bool {
	return now.Sub(e.LastOK) > StaleAfter || e.FailCount > e.OKCount
}

// WriteFileAtomic writes data to a temp file in the destination directory and
// renames it into place.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
