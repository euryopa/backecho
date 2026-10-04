package scan

import (
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Kimi is not implemented yet.
type Kimi struct{}

func (Kimi) ID() string { return "kimi" }

func (a Kimi) Discover(home string, env func(string) string) (Status, []Source) {
	return Status{Agent: a.ID(), State: "skipped", Detail: "adapter not implemented"}, nil
}

func (Kimi) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return nil, unrecognized("adapter not implemented")
}
