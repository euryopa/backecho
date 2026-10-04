package scan

import (
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// Qwen is not implemented yet.
type Qwen struct{}

func (Qwen) ID() string { return "qwen" }

func (a Qwen) Discover(home string, env func(string) string) (Status, []Source) {
	return Status{Agent: a.ID(), State: "skipped", Detail: "adapter not implemented"}, nil
}

func (Qwen) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return nil, unrecognized("adapter not implemented")
}
