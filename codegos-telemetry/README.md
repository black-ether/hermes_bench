# Codegos Hermes telemetry plugin v0.2.0

Passive observation only. Logs turn, tool, provider API and summarized stream events in JSONL without recording prompts, streamed text/reasoning, API request/response bodies or tool outputs.

This log is the telemetry input for the `hermes-bench` reporter in the parent directory; see `../README.md` for how the counters are interpreted.

## Installation

Replace the installed `codegos-telemetry` plugin's `__init__.py` and `plugin.yaml` with the files in this folder, then restart the Hermes process/gateway that loads plugins. Keep a backup of the previous plugin folder first. `plugin.yaml` must list every registered hook.

Log output defaults to `$HERMES_HOME/experiments/events.jsonl`. On native Windows, without `HERMES_HOME`, the directory is `%LOCALAPPDATA%\hermes\experiments`. `CODEGOS_TELEMETRY_DIR` overrides the directory.

## Optional reasoning delta observation

In Hermes configuration, under the **existing** `plugins:` section:

```yaml
plugins:
  stream_reasoning_deltas: true
```

This opt-in is optional. With it disabled, reasoning delta counts may remain zero even if the model performs reasoning. The plugin never writes reasoning content.

## Events

- `plugin.loaded`, `session.started`
- `turn.started`, `turn.response`, `turn.ended` (legacy names maintained)
- `tool.started`, `tool.completed` (legacy names maintained)
- `api.started`, `api.completed`, `api.error` (provider metadata and token counts, if supplied)
- `stream.started`, `stream.ended` (the latter has aggregate counts and observed delta arrival timing)

## Fields written by event

Every record carries `ts` (ISO 8601 UTC, millisecond precision) and `event`, plus whichever of these identity keys the hook received: `session_id`, `conversation_id`, `turn_id`, `task_id`, `api_request_id`, `tool_call_id`. Values that are `None` are omitted rather than written as null, so an absent key means "not supplied".

| Event | Additional fields |
|---|---|
| `plugin.loaded` | `version`, `log_path` |
| `session.started` | `model`, `platform` |
| `turn.started` | `model`, `platform`, `prompt_chars` |
| `tool.started` | `tool`, `arg_keys`, `arg_bytes`, `file_basenames` |
| `tool.completed` | `tool`, `arg_keys`, `arg_bytes`, `file_basenames`, `result_chars`, `result_bytes`, `duration_ms`, `status`, `error_type` |
| `turn.response` | `model`, `platform`, `response_chars` |
| `turn.ended` | `model`, `platform`, `completed`, `interrupted`, `wall_time_ms` |
| `api.started` | `api_call_count`, `retry_count`, `model`, `provider`, `api_mode`, `streaming`, `message_count`, `tool_count`, `approx_input_tokens`, `max_tokens` |
| `api.completed` | the `api.started` fields plus `api_duration` (seconds), `finish_reason`, `response_model`, `assistant_content_chars`, `assistant_tool_call_count`, and any of `usage_prompt_tokens`, `usage_completion_tokens`, `usage_total_tokens`, `usage_input_tokens`, `usage_output_tokens`, `usage_reasoning_tokens`, `usage_cache_read_tokens`, `usage_cache_write_tokens` that the provider supplied as numbers |
| `api.error` | the `api.started` fields plus `status_code`, `max_retries`, `retryable`, `reason`, `error_type` |
| `stream.started` | identity keys only |
| `stream.ended` | `finished`, `had_error`, `observed_duration_ms`, `first_delta_ms`, `first_text_ms`, `first_reasoning_ms`, `last_text_ms`, `last_reasoning_ms`, `max_delta_gap_ms`, `total_deltas`, `text_deltas`, `reasoning_deltas`, `text_chars`, `reasoning_chars` |

`arg_keys` / `arg_bytes` / `file_basenames` describe tool arguments without copying them: argument names, serialized byte size, and up to five path basenames. `error_type` comes from the error's `type` attribute; the message body is never persisted.

For `hermes-bench`, a `tool.completed` record with neither `status` nor `error_type` is reported as an unknown outcome, not a success. Set `status` explicitly if the host passes it through.

`api_duration` is the duration reported by Hermes. Stream timing is measured at callback reception, which can include asynchronous dispatch queue latency, and is **not** a direct measurement of hidden model thought or provider TTFT. Streams without correlation identifiers use a shared fallback and may be ambiguous under concurrency. The plugin intentionally does not fabricate per-request stream linkage.

Run a new test task and inspect the log to verify that callbacks actually fire in your active Hermes environment. Registration alone does not establish this.
