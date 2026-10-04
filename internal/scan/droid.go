package scan

import (
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Droid is not implemented yet.
type Droid struct{}

func (Droid) ID() string { return "droid" }

func (a Droid) Discover(home string, env func(string) string) (Status, []Source) {
	return Status{Agent: a.ID(), State: "skipped", Detail: "adapter not implemented"}, nil
}

func (Droid) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return nil, unrecognized("adapter not implemented")
}
