<div align="center">

# 🧩 Adapters

How backecho reads each tool's logs, and how much it can prove.

[README](../README.md) · [日本語 README](../README.ja.md) · [実装方針](PLAN.md)

</div>

---

Every adapter maps a vendor's native log onto one event:

```go
model.Event{Agent, Session, TS, Cwd, Branch, Kind, Command, Exit, StdoutHead, StderrHead, Path}
```

and nothing else. Fingerprinting, redaction, merge and render never see a vendor record.

## Ground rules

| Rule | Why |
| --- | --- |
| **Exit status comes only from a structured field, or an exit line the tool itself writes.** | `is_error: false`, `status: "success"` or "the model said it passed" are not exit 0. Unknown stays unknown and the command is dropped. |
| **Each adapter owns its parser and its fixtures.** | Vendor layouts change independently. A shared fuzzy parser would fail silently. |
| **Unknown schema → skip the file, count it.** | `backecho status` shows how many files an adapter could not read. The process never fails because of one vendor. |
| **SQLite is opened read-only** (`file:…?mode=ro`, `query_only`). | backecho never creates, migrates, checkpoints, or locks a vendor database. Rows are copied out and the connection is closed before render. |
| **A session belongs to the repo** if its recorded cwd is the git toplevel or below it — or, when the tool records no cwd, if an absolute edit path is inside it. | The log file's directory name is never used. |
| **Repo-local transcripts** (`.crush/crush.db`, `.aider.chat.history.md`) are read from those exact paths only. | backecho never walks the repo looking for logs. |

Legend: 🟢 verified against source or real logs, exit status recorded · 🟡 best effort, parses strictly · ⚪ tool records no exit status — edits only

<!-- adapters-detail:start -->
## Claude Code — `claude` 🟢

| | |
| --- | --- |
| Logs | `~/.claude/projects/<slug>/<session>.jsonl`, `~/.config/claude/projects/…` (subagent transcripts included) |
| Override | `CLAUDE_CONFIG_DIR` (replaces both; `projects/` under each) |
| Format | JSONL. Assistant lines hold `tool_use` blocks; the next user line holds the matching `tool_result` and a top-level `toolUseResult`. |
| Exit status | `toolUseResult.exitCode` when present; else a `tool_result` with `is_error: true` whose text begins `Exit code N`; else a non-error result whose `toolUseResult` is the Bash result object (`stdout`/`stderr`) → 0, because Claude Code reports a Bash call as an error exactly when it exits non-zero. |
| Dropped | Interrupted calls, timeouts, `run_in_background` and auto-backgrounded calls. |
| Edits | `Edit`, `Write`, `MultiEdit`, `NotebookEdit` → `file_path`; dropped when the result is an error. |
| Verified | Real Claude Code 2.x transcripts. |

## Codex CLI — `codex` 🟢

| | |
| --- | --- |
| Logs | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`, `~/.codex/archived_sessions/` |
| Override | `CODEX_HOME` |
| Format | JSONL `{timestamp, type, payload}`. `session_meta` / `turn_context` carry `cwd`; `response_item` carries `function_call`, `local_shell_call`, `custom_tool_call` and their outputs; `event_msg` carries `exec_command_end` and `patch_apply_end`. Pre-2025 rollouts without the envelope are read too. |
| Exit status | `exec_command_end.exit_code`; the JSON output's `metadata.exit_code`; or the header Codex writes into text output (`Exit code: N`, `Process exited with code N`). `Process running with session ID …` stays unknown. |
| Edits | `apply_patch` bodies (`*** Update File:` / `Add` / `Delete` / `Move to`), and successful `patch_apply_end.changes`. |
| Verified | Rollout format in the open-source `codex-rs`. |
<!-- adapters-detail:end -->

## Adding or fixing an adapter

1. **Capture a real log.** In a throwaway git repo, have the tool run a failing test, edit a file, and run the test again until it passes. Also run `ls -la`.
2. **Scrub it.** Replace the repo path with `{{ROOT}}`, the home directory with `{{HOME}}`, and remove usernames, hostnames, tokens, and output you would not post publicly. Keep record structure and field names intact.
3. **Plant the contract.** Add a failure line containing `Authorization: Bearer SECRETTOKEN123`, a call to `make test-unknown` with no result, and a session whose cwd is `{{ELSEWHERE}}` running `make test-elsewhere`.
4. **Write the test.** Place the fixture with `layout.place`, run `collect`, then `checkContract(t, "<id>", l, events, contract{paired: "<cmd>", after: "<path>"})`. For SQLite, build the database inside the test with `buildDB` — never commit a binary `.db`.
5. **Document it** in the type's doc comment and in the table above: where the logs live, the record shape, exactly where exit status comes from, and what is dropped.

```sh
CGO_ENABLED=0 go test ./internal/scan/ -run TestYourAgent -v
```
