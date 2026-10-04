package scan

import (
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Grok is not implemented yet.
type Grok struct{}

func (Grok) ID() string { return "grok" }

func (a Grok) Discover(home string, env func(string) string) (Status, []Source) {
	return Status{Agent: a.ID(), State: "skipped", Detail: "adapter not implemented"}, nil
}

func (Grok) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return nil, unrecognized("adapter not implemented")
}
