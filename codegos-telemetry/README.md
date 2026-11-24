# Codegos Hermes telemetry plugin v0.2.0

Passive observation only. Logs turn, tool, provider API and summarized stream events in JSONL without recording prompts, streamed text/reasoning, API request/response bodies or tool outputs.

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

`api_duration` is the duration reported by Hermes. Stream timing is measured at callback reception, which can include asynchronous dispatch queue latency, and is **not** a direct measurement of hidden model thought or provider TTFT. Streams without correlation identifiers use a shared fallback and may be ambiguous under concurrency. The plugin intentionally does not fabricate per-request stream linkage.

Run a new test task and inspect the log to verify that callbacks actually fire in your active Hermes environment. Registration alone does not establish this.
