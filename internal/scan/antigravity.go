package scan

import (
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Antigravity is not implemented yet.
type Antigravity struct{}

func (Antigravity) ID() string { return "antigravity" }

func (a Antigravity) Discover(home string, env func(string) string) (Status, []Source) {
	return Status{Agent: a.ID(), State: "skipped", Detail: "adapter not implemented"}, nil
}

func (Antigravity) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return nil, unrecognized("adapter not implemented")
}
