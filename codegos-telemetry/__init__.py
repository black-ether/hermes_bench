"""Passive, metadata-only Hermes telemetry for Codegos benchmarks.

Writes JSONL to HERMES_HOME/experiments/events.jsonl (or CODEGOS_TELEMETRY_DIR).
Never logs prompts, API payloads, tool outputs, streamed text, or reasoning text.
"""
from __future__ import annotations

import json
import os
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

VERSION = "0.2.0"
_LOCK = threading.RLock()
_TURN_STARTS: dict[str, float] = {}
_STREAMS: dict[str, dict[str, Any]] = {}
_MAX_STREAMS = 128


def _hermes_home() -> Path:
    if os.environ.get("HERMES_HOME"):
        return Path(os.path.expandvars(os.environ["HERMES_HOME"])).expanduser()
    if os.name == "nt" and os.environ.get("LOCALAPPDATA"):
        return Path(os.environ["LOCALAPPDATA"]) / "hermes"
    return Path.home() / ".hermes"


def _log_path() -> Path:
    override = os.environ.get("CODEGOS_TELEMETRY_DIR")
    directory = Path(os.path.expandvars(override)).expanduser() if override else _hermes_home() / "experiments"
    return directory / "events.jsonl"


def _write_event(event: str, **fields: Any) -> None:
    """Write a single JSONL record; failures never abort Hermes."""
    record = {
        "ts": datetime.now(timezone.utc).isoformat(timespec="milliseconds"),
        "event": event,
        **{key: value for key, value in fields.items() if value is not None},
    }
    try:
        path = _log_path()
        line = json.dumps(record, ensure_ascii=False, separators=(",", ":"), default=str)
        with _LOCK:
            path.parent.mkdir(parents=True, exist_ok=True)
            with path.open("a", encoding="utf-8", newline="\n") as stream:
                stream.write(line + "\n")
    except Exception:
        # Telemetry must not disrupt the agent. Avoid printing payloads/errors that
        # might contain sensitive paths or provider details.
        pass


def _ids(kwargs: dict[str, Any]) -> dict[str, Any]:
    return {k: kwargs[k] for k in (
        "session_id", "conversation_id", "turn_id", "task_id", "api_request_id", "tool_call_id"
    ) if kwargs.get(k) not in (None, "")}


def _safe_basename(value: Any) -> str | None:
    if not isinstance(value, str) or not value.strip():
        return None
    return value.replace("\\", "/").rstrip("/").split("/")[-1][:160] or None


def _arg_metadata(args: Any) -> dict[str, Any]:
    if not isinstance(args, dict):
        return {"args_type": type(args).__name__}
    names = [_safe_basename(args.get(k)) for k in ("path", "file_path", "filepath", "filename", "file")]
    try:
        count = len(json.dumps(args, ensure_ascii=False, default=str).encode("utf-8"))
    except Exception:
        count = None
    return {
        "arg_keys": sorted(str(k) for k in args),
        "arg_bytes": count,
        "file_basenames": sorted(set(n for n in names if n)) or None,
    }


def _text_size(value: Any) -> dict[str, int] | dict[str, None]:
    if value is None:
        return {"result_chars": None, "result_bytes": None}
    try:
        s = value if isinstance(value, str) else json.dumps(value, default=str)
        return {"result_chars": len(s), "result_bytes": len(s.encode("utf-8"))}
    except Exception:
        return {"result_chars": None, "result_bytes": None}


def _turn_key(session_id: Any, kwargs: dict[str, Any]) -> str:
    # pre_llm_call and on_session_end currently expose session_id reliably.
    return str(kwargs.get("turn_id") or session_id or kwargs.get("task_id") or "unknown")


def on_session_start(session_id: str = "", model: str = "", platform: str = "", **kwargs: Any) -> None:
    _write_event("session.started", session_id=session_id or None, model=model or None,
                 platform=platform or None, **_ids({k: v for k, v in kwargs.items() if k != "session_id"}))


def on_turn_start(session_id: str = "", user_message: Any = None,
                  model: str = "", platform: str = "", **kwargs: Any) -> None:
    key = _turn_key(session_id, kwargs)
    with _LOCK:
        _TURN_STARTS[key] = time.monotonic()
    if isinstance(user_message, str):
        prompt_chars = len(user_message)
    elif isinstance(user_message, list):
        prompt_chars = sum(len(str(p.get("text", ""))) for p in user_message if isinstance(p, dict))
    else:
        prompt_chars = None
    _write_event("turn.started", session_id=session_id or None, model=model or None,
                 platform=platform or None, prompt_chars=prompt_chars, **_ids(kwargs))


def on_tool_start(tool_name: str = "", args: Any = None, task_id: str = "", **kwargs: Any) -> None:
    _write_event("tool.started", tool=tool_name, **_ids({**kwargs, "task_id": task_id or kwargs.get("task_id")}),
                 **_arg_metadata(args))


def on_tool_end(tool_name: str = "", args: Any = None, result: Any = None,
                task_id: str = "", duration_ms: int | float | None = None, **kwargs: Any) -> None:
    _write_event("tool.completed", tool=tool_name,
                 **_ids({**kwargs, "task_id": task_id or kwargs.get("task_id")}),
                 **_arg_metadata(args), **_text_size(result), duration_ms=duration_ms,
                 status=kwargs.get("status"), error_type=kwargs.get("error_type"))


def on_turn_response(session_id: str = "", assistant_response: str = "",
                     model: str = "", platform: str = "", **kwargs: Any) -> None:
    _write_event("turn.response", session_id=session_id or None, model=model or None,
                 platform=platform or None, response_chars=len(assistant_response or ""), **_ids(kwargs))


def on_turn_end(session_id: str = "", completed: bool = False, interrupted: bool = False,
                model: str = "", platform: str = "", **kwargs: Any) -> None:
    with _LOCK:
        started = _TURN_STARTS.pop(_turn_key(session_id, kwargs), None)
    elapsed = round((time.monotonic() - started) * 1000) if started is not None else None
    _write_event("turn.ended", session_id=session_id or None, model=model or None,
                 platform=platform or None, completed=completed, interrupted=interrupted,
                 wall_time_ms=elapsed, **_ids(kwargs))


def _usage_metadata(usage: Any) -> dict[str, Any]:
    """Whitelist numeric token accounting only; provider-specific extra data is ignored."""
    if not isinstance(usage, dict):
        return {}
    allowed = ("prompt_tokens", "completion_tokens", "total_tokens", "input_tokens",
               "output_tokens", "reasoning_tokens", "cache_read_tokens", "cache_write_tokens")
    return {"usage_" + k: usage[k] for k in allowed if type(usage.get(k)) in (int, float)}


def _api_fields(kwargs: dict[str, Any]) -> dict[str, Any]:
    return {
        **_ids(kwargs),
        "api_call_count": kwargs.get("api_call_count"),
        "retry_count": kwargs.get("retry_count"),
        "model": kwargs.get("model"),
        "provider": kwargs.get("provider"),
        "api_mode": kwargs.get("api_mode"),
        "streaming": kwargs.get("streaming"),
    }


def on_api_start(**kwargs: Any) -> None:
    _write_event("api.started", **_api_fields(kwargs),
                 message_count=kwargs.get("message_count"), tool_count=kwargs.get("tool_count"),
                 approx_input_tokens=kwargs.get("approx_input_tokens"), max_tokens=kwargs.get("max_tokens"))


def on_api_end(**kwargs: Any) -> None:
    # api_duration is Hermes' own reported field (seconds); retain the original
    # unit/name rather than inferring duration from asynchronously dispatched hooks.
    _write_event("api.completed", **_api_fields(kwargs),
                 api_duration=kwargs.get("api_duration"),
                 finish_reason=kwargs.get("finish_reason"),
                 response_model=kwargs.get("response_model"),
                 assistant_content_chars=kwargs.get("assistant_content_chars"),
                 assistant_tool_call_count=kwargs.get("assistant_tool_call_count"),
                 **_usage_metadata(kwargs.get("usage")))


def on_api_error(**kwargs: Any) -> None:
    error = kwargs.get("error")
    # Never persist error.message, error body or request: may contain credentials.
    error_type = error.get("type") if isinstance(error, dict) else None
    _write_event("api.error", **_api_fields(kwargs),
                 status_code=kwargs.get("status_code"),
                 max_retries=kwargs.get("max_retries"),
                 retryable=kwargs.get("retryable"), reason=kwargs.get("reason"),
                 error_type=error_type)


def _stream_key(kwargs: dict[str, Any]) -> str:
    # Prefer explicit identity, then session. No invented linkage to API calls.
    for key in ("stream_id", "api_request_id", "turn_id", "session_id"):
        if kwargs.get(key):
            return f"{key}:{kwargs[key]}"
    return "unidentified"


def on_stream_start(**kwargs: Any) -> None:
    key = _stream_key(kwargs)
    with _LOCK:
        if len(_STREAMS) >= _MAX_STREAMS and key not in _STREAMS:
            _STREAMS.pop(next(iter(_STREAMS)))
        _STREAMS[key] = {
            "start": time.monotonic(), "last": None,
            "total_deltas": 0, "text_deltas": 0, "reasoning_deltas": 0,
            "text_chars": 0, "reasoning_chars": 0,
            "first_delta_ms": None, "first_text_ms": None, "first_reasoning_ms": None,
            "last_text_ms": None, "last_reasoning_ms": None,
            "max_gap_ms": 0,
        }
    _write_event("stream.started", **_ids(kwargs))


def on_stream_delta(delta: Any = None, kind: str = "text", **kwargs: Any) -> None:
    now = time.monotonic()
    key = _stream_key(kwargs)
    category = "reasoning" if kind == "reasoning" else "text"
    n = len(delta) if isinstance(delta, str) else 0
    with _LOCK:
        s = _STREAMS.get(key)
        if s is None:
            # Unmatched deltas should not crash telemetry or invent a start time.
            return
        elapsed = round((now - s["start"]) * 1000)
        if s["first_delta_ms"] is None:
            s["first_delta_ms"] = elapsed
        if s["last"] is not None:
            s["max_gap_ms"] = max(s["max_gap_ms"], round((now - s["last"]) * 1000))
        s["last"] = now
        s["total_deltas"] += 1
        s[f"{category}_deltas"] += 1
        s[f"{category}_chars"] += n
        if s[f"first_{category}_ms"] is None:
            s[f"first_{category}_ms"] = elapsed
            
        s[f"last_{category}_ms"] = elapsed


def on_stream_end(final_text: Any = None, finished: bool | None = None,
                  error: Any = None, **kwargs: Any) -> None:
    key = _stream_key(kwargs)
    end = time.monotonic()
    with _LOCK:
        s = _STREAMS.pop(key, None)
    fields: dict[str, Any] = {"finished": finished, "had_error": bool(error)}
    if s is not None:
        fields.update(
            observed_duration_ms=round((end - s["start"]) * 1000),
            first_delta_ms=s["first_delta_ms"],
            first_text_ms=s["first_text_ms"],
            first_reasoning_ms=s["first_reasoning_ms"],
            last_text_ms=s["last_text_ms"],
            last_reasoning_ms=s["last_reasoning_ms"],
            max_delta_gap_ms=s["max_gap_ms"] if s["last"] is not None else None,
            total_deltas=s["total_deltas"], text_deltas=s["text_deltas"],
            reasoning_deltas=s["reasoning_deltas"],
            text_chars=s["text_chars"], reasoning_chars=s["reasoning_chars"],
        )
    # final_text and error contents must never be written.
    _write_event("stream.ended", **_ids(kwargs), **fields)


def register(ctx: Any) -> None:
    """Register supported Hermes observer hooks, without changing agent behavior."""
    for hook_name, callback in (
        ("on_session_start", on_session_start),
        ("pre_llm_call", on_turn_start),
        ("pre_tool_call", on_tool_start),
        ("post_tool_call", on_tool_end),
        ("post_llm_call", on_turn_response),
        ("on_session_end", on_turn_end),
        ("pre_api_request", on_api_start),
        ("post_api_request", on_api_end),
        ("api_request_error", on_api_error),
        ("on_stream_start", on_stream_start),
        ("on_stream_delta", on_stream_delta),
        ("on_stream_end", on_stream_end),
    ):
        ctx.register_hook(hook_name, callback)
    _write_event("plugin.loaded", version=VERSION, log_path=str(_log_path()))
