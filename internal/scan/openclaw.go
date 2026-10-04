package scan

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/euryopa/backecho/internal/model"
)

// OpenClaw reads OpenClaw (openclaw/openclaw) session transcripts:
// $OPENCLAW_STATE_DIR/agents/<agentId>/sessions/<sessionId>.jsonl, default
// ~/.openclaw. OPENCLAW_STATE_DIR is comma-separated for several roots.
//
// OpenClaw is built on the pi-agent core and writes the same JSONL: a
// {type:"session", id, cwd, timestamp} header, then {type:"message"} lines
// with toolCall blocks and toolResult messages; parsing is shared with Pi
// (piParseSession). Its shell tool is "exec" {command, workdir}; the result
// details are {status:"completed"|"failed", exitCode, cwd, aggregated}, and
// details.exitCode is the only exit source for exec (a "running" or
// approval-pending status is unknown). Older builds that still use pi's
// "bash" tool follow the Pi rules.
//
// Not parsed: current builds keep live session rows and transcripts in
// SQLite (agents/<agentId>/agent/openclaw-agent.sqlite); only the JSONL
// transcripts under agents/<agentId>/sessions/ (legacy and archived) are
// read. Archived variants with a suffix after .jsonl are skipped.
type OpenClaw struct{}

func (OpenClaw) ID() string { return "openclaw" }

func (o OpenClaw) Discover(home string, env func(string) string) (Status, []Source) {
	var dirs []string
	for _, r := range roots(env, "OPENCLAW_STATE_DIR", filepath.Join(home, ".openclaw")) {
		dirs = append(dirs, filepath.Join(r, "agents"))
	}
	return discoverFiles(o.ID(), dirs, openclawSessionFile)
}

func (o OpenClaw) Parse(src Source, repoRoot string, since time.Time) ([]model.Event, error) {
	return piParseSession(o.ID(), src.Path, repoRoot, since)
}

// openclawSessionFile matches agents/<id>/sessions/*.jsonl.
func openclawSessionFile(p string, _ fs.DirEntry) bool {
	return strings.HasSuffix(p, ".jsonl") && filepath.Base(filepath.Dir(p)) == "sessions"
}
