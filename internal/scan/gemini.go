package scan

import (
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Gemini is not implemented yet.
type Gemini struct{}

func (Gemini) ID() string { return "gemini" }

func (a Gemini) Discover(home string, env func(string) string) (Status, []Source) {
	return Status{Agent: a.ID(), State: "skipped", Detail: "adapter not implemented"}, nil
}

func (Gemini) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return nil, unrecognized("adapter not implemented")
}
