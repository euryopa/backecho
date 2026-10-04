package norm

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// PairWindow is how far apart a failure and the success that resolves it
// may be.
const PairWindow = 30 * time.Minute

const maxAfter = 8

// SessionKey is the opaque, stable key the store keeps for a session.
func SessionKey(agent, session string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + session))
	return hex.EncodeToString(sum[:])[:12]
}

type pendingFail struct {
	idx  int // position in the session
	n    Normalized
	ts   time.Time
	hint string
	obs  int // index into the output, -1 when the failure was already seen
}

type editRec struct {
	idx  int
	path string
}

// Keep turns events into observations. seen[i] marks events that an earlier
// sync already counted: they still take part in pairing, so a failure from
// the last sync can be resolved by a success in this one, but they produce no
// new observation. seen may be nil.
//
// Events must already be filtered to the repo. Within a session they are taken
// in the order given, which is the order of the log.
func Keep(events []model.Event, seen []bool, home, root string) ([]model.Observation, []model.Resolved) {
	type sess struct {
		agent, key string
		idx        []int
	}
	var order []string
	groups := map[string]*sess{}
	for i, e := range events {
		k := e.Agent + "\x00" + e.Session
		g := groups[k]
		if g == nil {
			g = &sess{agent: e.Agent, key: SessionKey(e.Agent, e.Session)}
			groups[k] = g
			order = append(order, k)
		}
		g.idx = append(g.idx, i)
	}

	var out []model.Observation
	var resolved []model.Resolved
	for _, k := range order {
		g := groups[k]
		var pending []pendingFail
		var edits []editRec
		for pos, i := range g.idx {
			e := events[i]
			old := seen != nil && seen[i]
			switch e.Kind {
			case model.KindEdit:
				if e.Path == "" || SensitivePath(e.Path) {
					continue
				}
				if rel, ok := RelPath(root, e.Cwd, e.Path); ok {
					edits = append(edits, editRec{idx: pos, path: rel})
				}
				continue
			case model.KindShell:
			default:
				continue
			}
			if e.Exit == nil || e.Command == "" || SensitiveCommand(e.Command) {
				continue
			}
			n, ok := Normalize(e.Command, e.Cwd, home, root)
			if !ok || n.Command == "" {
				continue
			}
			sh := Analyze(n.Command)
			if sh.Denied || sh.LongRunning || sh.Unreliable {
				continue
			}

			if *e.Exit != 0 {
				hint := e.StderrHead
				if hint == "" {
					hint = e.StdoutHead
				}
				hint = Head(hint)
				pf := pendingFail{idx: pos, n: n, ts: e.TS, hint: hint, obs: -1}
				if !old {
					pf.obs = len(out)
					out = append(out, model.Observation{
						ID: n.ID, Command: n.Command, Cwd: n.Cwd, Class: n.Class, Env: n.Env,
						Agent: e.Agent, Session: g.key, TS: e.TS, OK: false, Hint: hint, Countable: true,
					})
				}
				pending = append(pending, pf)
				continue
			}

			// Success: resolve every pending failure it is about.
			var match *pendingFail
			kept := pending[:0]
			for j := range pending {
				f := pending[j]
				if f.n.Cwd == n.Cwd && within(f.ts, e.TS) && SharedToken(f.n.Command, n.Command) {
					if f.obs >= 0 {
						out[f.obs].Consumed = true
					} else if !old {
						resolved = append(resolved, model.Resolved{ID: f.n.ID, Session: g.key, TS: f.ts})
					}
					if match == nil || f.idx > match.idx {
						fc := f
						match = &fc
					}
					continue
				}
				kept = append(kept, f)
			}
			pending = kept
			if old {
				continue
			}
			o := model.Observation{
				ID: n.ID, Command: n.Command, Cwd: n.Cwd, Class: n.Class, Env: n.Env,
				Agent: e.Agent, Session: g.key, TS: e.TS, OK: true, Eligible: sh.ProjectVerb,
			}
			if match != nil {
				o.Eligible, o.Paired = true, true
				o.Was, o.WasTS = match.hint, match.ts
				var after []string
				for _, ed := range edits {
					if ed.idx > match.idx && ed.idx < pos {
						after = append(after, ed.path)
					}
				}
				o.After = Union(nil, after, maxAfter)
			}
			out = append(out, o)
		}
	}
	return out, resolved
}

func within(fail, ok time.Time) bool {
	if fail.IsZero() || ok.IsZero() {
		return true
	}
	d := ok.Sub(fail)
	return d >= 0 && d <= PairWindow
}
