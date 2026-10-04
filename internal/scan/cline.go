package scan

import (
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Cline is not implemented yet.
type Cline struct{}

func (Cline) ID() string { return "cline" }

func (a Cline) Discover(home string, env func(string) string) (Status, []Source) {
	return Status{Agent: a.ID(), State: "skipped", Detail: "adapter not implemented"}, nil
}

func (Cline) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return nil, unrecognized("adapter not implemented")
}
