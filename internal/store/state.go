package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Handle is the cursor for one log file or database.
//
// Logs are append-only in practice but tool results arrive after their calls,
// and some vendors rewrite a whole JSON file per turn. So instead of a byte
// offset the cursor keeps how many repo events were already counted: a
// changed handle is parsed again, the first Events events take part in
// pairing but are not counted twice.
type Handle struct {
	Agent  string    `json:"agent"`
	Path   string    `json:"path"`
	Kind   string    `json:"kind"`
	Size   int64     `json:"size"`
	Mtime  time.Time `json:"mtime"`
	Events int       `json:"events"`
}

// State is .backecho/state.json.
type State struct {
	Version int                `json:"version"`
	Handles map[string]*Handle `json:"handles"`
	// GitignoreWarned records that the "no .gitignore" warning was shown.
	GitignoreWarned bool `json:"gitignore_warned,omitempty"`
}

func HandleKey(agent, path string) string { return agent + "|" + path }

// LoadState reads state.json; a missing file is an empty state.
func LoadState(dir string) (*State, error) {
	st := &State{Version: Version, Handles: map[string]*Handle{}}
	b, err := os.ReadFile(filepath.Join(dir, StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		// A corrupt cursor only costs a rescan; the store dedupes nothing
		// from it, so start over rather than fail.
		return &State{Version: Version, Handles: map[string]*Handle{}}, nil
	}
	if st.Handles == nil {
		st.Handles = map[string]*Handle{}
	}
	return st, nil
}

// SaveState writes state.json atomically.
func SaveState(dir string, st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(dir, StateFile), append(b, '\n'))
}

// Plan says what to do with a handle given its current size and mtime.
type Plan struct {
	Skip bool // unchanged since the last sync
	Seen int  // events already counted; 0 when the handle must be rescanned
}

// PlanFor compares a handle with what the last sync recorded. A size shrink
// or an mtime that went backwards rescans the handle from the start.
func (s *State) PlanFor(agent, path string, size int64, mtime time.Time) Plan {
	h := s.Handles[HandleKey(agent, path)]
	if h == nil {
		return Plan{}
	}
	if h.Size == size && h.Mtime.Equal(mtime) {
		return Plan{Skip: true}
	}
	if size < h.Size || mtime.Before(h.Mtime) {
		return Plan{}
	}
	return Plan{Seen: h.Events}
}

// Record stores the cursor after a parse.
func (s *State) Record(agent, path, kind string, size int64, mtime time.Time, events int) {
	s.Handles[HandleKey(agent, path)] = &Handle{Agent: agent, Path: path, Kind: kind, Size: size, Mtime: mtime, Events: events}
}
