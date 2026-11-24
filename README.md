# Hermes Bench v0.8 — compact terminal + workload estimates (source release)

Deterministic Go reporter for Hermes telemetry. This release contains **source code only**; build locally with Go 1.23 or newer. It does not modify the Hermes database or telemetry log.

## Quick start (PowerShell)

From this folder, with Go installed and `hermes` available on your PATH:

```powershell
go run . run
```

This increments the locally exported sessions and creates a Markdown report in `./reports/`. With `go run .`, Go automatically compiles to a temporary executable; no separate build step is needed.

Alternatively:

```powershell
go test ./...
go build -o hermes-bench.exe .
.\hermes-bench.exe run
```

An existing `%LOCALAPPDATA%\hermes-bench\config.json` is reused. If this is the first installation, run `go run . init` once.

## Commands

| Command | Action |
|---|---|
| `go run .` / `go run . list` | List locally exported sessions |
| `go run . sync` | Export new or changed telemetry-attributed sessions only |
| `go run . sync --all` | Full Hermes export, including sessions without telemetry |
| `go run . report latest` | Print and save a report for the latest exported session |
| `go run . report 1` | Print and save a report by listing index |
| `go run . run` | Incremental sync followed by `report latest` |

Compiled versions use `hermes-bench.exe` in place of `go run .`.

## Detailed Markdown retained from v0.6

The Markdown report now includes:

- A **Stored conversation transcript** appendix containing all available user prompts, saved assistant response text, and saved assistant reasoning text. Each stored message has its role, timestamp (if present), gap since the preceding timestamped message, active/archive and compacted status, stored `token_count`, tool-call names, and `finish_reason` when available. Tool arguments and tool result bodies are deliberately omitted from the Markdown appendix.
- A **Per-request provider usage** table derived from the telemetry plugin's `api.completed` records. Individual input, output, reasoning, cache-read, cache-write, and total token fields appear only if Hermes supplied them. Missing values show `n/a` and are never interpreted as zero.
- A measured **active-message timestamp span**, and **first active user-to-assistant message gap**, when stored timestamps exist, with fractional-second precision. These are message-recording gaps, not the model's internal thinking time.
- An optional per-role summary of persisted `token_count` values from active messages. Those are **not** subcomponents of cumulative API prompt tokens because the model can receive shared system context and repeated history for every request.

The terminal output stays concise, and raw prompts/reasoning are **never printed to the terminal**.

**Security:** Even with Hermes's `--redact` export, the detailed Markdown can expose project code, prompts, private data, or saved model reasoning. Reports are local; inspect them before sharing. Prior raw exports are not verified to have been redacted. Files from older sessions without a reasoning sidecar show an explicit unavailable marker rather than invented text. `reasoning`/`reasoning_content` mirrors are not concatenated to avoid duplication.

The example session provided with the repository has no reasoning sidecar or message timestamps. The new appendix works there, but those fields will be unavailable; the tests use synthetic records to exercise both plain-text and structured reasoning storage.

## Terminal summary (v0.8)

The terminal follows a single-line narrative with an aligned list of tool timings, a three-part workload estimate and a line of provider usage counters. No raw content is printed to the console.

Illustrative layout (8ms kickoff and per-tool token estimates below are **sample inputs**, not observations from the user's local experiment):

```text
hy4-flash completed the task in 83.0s (8ms before first model request; 23 tool calls across 9 API calls; $0.0000), spending 17.6s waiting on tools (37.1s total across individual tool calls):
  search_files   18.52s | ~6.2k toks
  read_file      16.42s | ~17.1k toks
  terminal        1.79s | ~620 toks
  skill_view      330ms | ~2.8k toks
using ~43 prompt toks (x9 = ~387 cumulative), ~266.2k background workload toks, and ~950 final-answer toks.
  API: 264.2k total input (46.1k uncached, 218.1k cached), 3.3k output (1.2k reported reasoning; not added again).
Telemetry matches.
Full report: reports/Inspect project configuration loading process.md
```

### What the token numbers mean

- **Prompt and final-answer tokens:** very rough *text-only estimates* made from persisted session `content`. ASCII characters are divided by four (rounded up); CJK script characters are counted individually. **No timestamps, JSON keys, tool payloads, or reasoning sidecars** are included in these two estimates. The estimated final-answer text count is **not** the provider's entire completion-token count.
- **Prompt x API requests:** an estimated count of prompt-token *occurrences* assuming each API request included the original prompt unchanged. This is not verified against provider request payloads. Sessions with more than one active user message do not get this estimate.
- **Background workload:** `sum(provider_input_tokens + provider_output_tokens) - (estimated_prompt_tokens * completed_requests) - estimated_final_answer_tokens`. It includes cached/repeated context, system instructions, tool results, intermediate generated output and reasoning; it is **not** the number of reasoning tokens or distinct tokens. Shown only when every API request provides counters that reconcile with the session totals, and the session has a single user prompt and saved final answer.
- **Tool-result token estimates:** `result_chars / 4`, rounded up, from the telemetry plugin. These are not exact model-tokenizer counts, and they are **not additive** to cumulative API input because context may be reused. When character counts are unavailable, display measured result KB instead of inventing tokens.
- **Provider usage:** total input counts across requests include cache-read input; uncached and cached sums are shown separately. Reasoning tokens are already within output on the confirmed HY4 Flash session and are **not** added a second time.
- **Start delay:** computed from the first persisted user-message timestamp to the first `api.started` telemetry timestamp; if that is absent, the first `turn.started` timestamp is identified explicitly. Never replace it with average API duration or first persisted assistant-message delay. Unavailable and out-of-order timestamps are not guessed.
- **Tool elapsed time:** 17.6s in the sample is tool-busy *union* time; 37.1s is summed tool execution time. Other agent/provider activity can overlap with tool execution, so “waiting on tools” is a conversational label, not a pure idle-time measurement.

The detailed Markdown preserves original provider counters, a per-request table, saved transcript and reasoning where present, and the calculation's assumptions. The provider name is derived from telemetry, e.g. `nous` → `Nous`, with no vendor mapping.

## Report filenames and history (new in v0.7)

Latest exported session: `reports/<task title>.md`, with Windows-invalid filename characters replaced. Task title capitalization and spaces are preserved. A same-session report can be regenerated without changing its name.

Every generated report is also archived at `reports/history/<task title> (<session hash>).md`. Different sessions with identical task titles therefore retain separate complete reports. Reporting an older session by index creates/updates its history file **without changing** the latest title-only report. A later `run` or `report latest` updates the title-only file for its task.

Custom `reports_dir` settings are honored; all paths above are relative to that directory.

## Incremental behavior

On its first incremental sync, v0.6 **adopts the existing telemetry log as a baseline**, and exports only the most recently observed session. This avoids replaying months of accumulated events. Use `sync --all` if you want to import all previously stored Hermes sessions. On later syncs, the reporter exports each session whose number of recorded events has increased since its previous sync. It writes separate redacted session snapshots to `sessions_dir` and stores its progress in a `.hermes-bench-sync.json` file there.

It does not require `/status` or copying session IDs. It does not delete Hermes sessions or modify `events.jsonl`. A previously deleted Hermes session cannot be exported retrospectively, although telemetry remains. If a run has no session-attributed telemetry, it cannot be discovered incrementally; `sync --all` is the fallback.

The reporter assumes no manual truncation/replacement of `events.jsonl`; if you rotate or delete the file, reset the incremental state file or run `sync --all`. Repeated runs inside the same session are exported again only if additional attributed events were recorded. Source snapshots and the Hermes database remain local.

## Reports location

- With a compiled binary: `<directory containing hermes-bench.exe>/reports/`.
- With `go run .`: `<working directory>/reports/`.
- Explicit custom `reports_dir` settings in the JSON configuration remain respected.

## Timing and scope

One fresh Hermes session per experimental run is recommended. The reporter still supports multi-turn conversations but totals are session-wide; multiple user messages trigger a warning. It supports API and stream timing fields including `first_reasoning_ms`, `last_reasoning_ms`, `first_text_ms`, `last_text_ms`. Old telemetry lacking last-chunk values remains readable.

Hermes' `--redact` export masks secrets but may preserve private task text. Do not share session exports without reviewing them.
