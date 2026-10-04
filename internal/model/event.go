// Package model holds the agent-blind types shared by every stage of the
// pipeline: the event adapters emit, the observations norm derives from it,
// and the store that is rendered to markdown.
package model

import "time"

type Kind string

const (
	KindShell Kind = "shell"
	KindEdit  Kind = "edit"
)

// Event is the only thing an adapter emits. Downstream packages never see a
// vendor record.
type Event struct {
	Agent      string
	Session    string
	TS         time.Time // zero if the record has no timestamp; caller may fill mtime
	Cwd        string    // absolute, as recorded; empty if unknown
	Branch     string
	Kind       Kind
	Command    string // shell only
	Exit       *int   // nil means unknown; never treat nil as 0
	StdoutHead string // first line, max 200 chars, redacted
	StderrHead string
	Path       string // edit only
}

// IntPtr is a convenience for adapters and tests.
func IntPtr(i int) *int { return &i }

const (
	ClassVerify = "verify"
	ClassLocal  = "local"
)

// Observation is one normalized shell result, ready to merge into the store.
type Observation struct {
	ID      string
	Command string
	Cwd     string
	Class   string
	Env     []string
	Agent   string
	Session string // opaque key, already hashed
	TS      time.Time
	OK      bool

	// Eligible is true for a success that may create an entry on its own: a
	// bare project verb, or a success that resolved an earlier failure.
	Eligible bool
	// Paired is true when this success resolved a failure in the same session.
	Paired bool
	Was    string
	WasTS  time.Time
	After  []string

	// Hint is the one-line failure output for a non-zero result.
	Hint string
	// Consumed marks a failure that a later success in the same session
	// resolved. It never becomes an open failure.
	Consumed bool
	// Countable marks a failure that may bump fail_count of an existing entry.
	Countable bool
}

// Resolved identifies an open failure that a newer success resolved.
type Resolved struct {
	ID      string
	Session string
	TS      time.Time
}

// Entry is one fact in the store.
type Entry struct {
	ID          string     `json:"id"`
	Command     string     `json:"command"`
	Cwd         string     `json:"cwd"`
	Class       string     `json:"class"`
	OKCount     int        `json:"ok_count"`
	FailCount   int        `json:"fail_count"`
	LastOK      time.Time  `json:"last_ok"`
	LastFail    *time.Time `json:"last_fail,omitempty"`
	Was         string     `json:"was,omitempty"`
	After       []string   `json:"after,omitempty"`
	Sessions    int        `json:"sessions"`
	Agents      []string   `json:"agents"`
	LastAgent   string     `json:"last_agent,omitempty"`
	Env         []string   `json:"env,omitempty"`
	StaleSince  *time.Time `json:"stale_since,omitempty"`
	SessionKeys []string   `json:"session_keys,omitempty"`
}

// Open is a failure with no later success in its session.
type Open struct {
	ID      string    `json:"id"`
	Command string    `json:"command"`
	Cwd     string    `json:"cwd"`
	Class   string    `json:"class"`
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	TS      time.Time `json:"ts"`
	Hint    string    `json:"hint,omitempty"`
}

// Store is the source of truth. Markdown is a render of it.
type Store struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
	Open    []Open  `json:"open,omitempty"`
}

// AdapterStatus is what `backecho status` reports for one adapter.
type AdapterStatus struct {
	Agent   string `json:"agent"`
	State   string `json:"state"` // "found", "empty", "skipped"
	Detail  string `json:"detail,omitempty"`
	Sources int    `json:"sources"`
	Events  int    `json:"events"`
}
