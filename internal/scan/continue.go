package scan

import (
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Continue is not implemented yet.
type Continue struct{}

func (Continue) ID() string { return "continue" }

func (a Continue) Discover(home string, env func(string) string) (Status, []Source) {
	return Status{Agent: a.ID(), State: "skipped", Detail: "adapter not implemented"}, nil
}

func (Continue) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return nil, unrecognized("adapter not implemented")
}
