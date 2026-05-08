#!/usr/bin/env python3
"""Build a tiny browser-loadable data file from local Codex session logs.

The script intentionally reads only metadata and usage records:
- session_meta.cwd / id
- turn_context.model
- event_msg token_count info/rate_limits
- task_complete duration
- error/abort counts

It does not export prompt text, assistant messages, tool output, or file content.
"""

from __future__ import annotations

import argparse
import json
import math
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any


USAGE_KEYS = (
    "input_tokens",
    "cached_input_tokens",
    "output_tokens",
    "reasoning_output_tokens",
    "total_tokens",
)


def parse_time(value: str | None) -> datetime | None:
    if not value:
        return None
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None


def fmt_int(value: int | float) -> str:
    value = int(round(value))
    if abs(value) >= 1_000_000_000:
        return f"{value / 1_000_000_000:.2f}B"
    if abs(value) >= 1_000_000:
        return f"{value / 1_000_000:.2f}M"
    if abs(value) >= 1_000:
        return f"{value / 1_000:.0f}K"
    return str(value)


def fmt_duration(seconds: int | float | None) -> str:
    if seconds is None:
        return "未知"
    seconds = max(0, int(seconds))
    hours, rem = divmod(seconds, 3600)
    minutes, _ = divmod(rem, 60)
    if hours:
        return f"{hours}h {minutes:02d}m"
    return f"{minutes}m"


def display_time(ts: datetime | None) -> str:
    if not ts:
        return "--"
    return ts.astimezone().strftime("%H:%M")


def project_name(cwd: str | None, fallback: str) -> str:
    if not cwd:
        return fallback
    name = Path(cwd).name.strip()
    return name or fallback


@dataclass
class SessionStats:
    sid: str
    file: str
    cwd: str | None = None
    model: str = "unknown"
    started_at: datetime | None = None
    ended_at: datetime | None = None
    duration_ms: int = 0
    ttfb_ms: int = 0
    ttfb_count: int = 0
    calls: int = 0
    completions: int = 0
    failures: int = 0
    usage: dict[str, int] = field(default_factory=lambda: {key: 0 for key in USAGE_KEYS})
    prev_total_usage: dict[str, int] | None = None


def add_usage(dst: dict[str, int], src: dict[str, Any] | None) -> None:
    if not isinstance(src, dict):
        return
    for key in USAGE_KEYS:
        value = src.get(key)
        if isinstance(value, (int, float)) and math.isfinite(value):
            dst[key] += int(value)


def usage_snapshot(src: dict[str, Any] | None) -> dict[str, int] | None:
    if not isinstance(src, dict):
        return None
    usage: dict[str, int] = {}
    has_value = False
    for key in USAGE_KEYS:
        value = src.get(key)
        if isinstance(value, (int, float)) and math.isfinite(value):
            usage[key] = max(0, int(value))
            has_value = True
        else:
            usage[key] = 0
    return usage if has_value else None


def usage_delta(current: dict[str, int], previous: dict[str, int]) -> dict[str, int] | None:
    usage = {key: max(0, current.get(key, 0) - previous.get(key, 0)) for key in USAGE_KEYS}
    return usage if any(usage.values()) else None


def safe_percent(value: Any) -> float | None:
    if isinstance(value, (int, float)) and math.isfinite(value):
        return max(0.0, min(100.0, float(value)))
    return None


def load_sessions(root: Path, cutoff: datetime) -> tuple[list[SessionStats], list[dict[str, Any]], dict[str, Any] | None, list[dict[str, Any]], list[dict[str, Any]]]:
    sessions: list[SessionStats] = []
    events: list[dict[str, Any]] = []
    ttfb_events: list[dict[str, Any]] = []
    failure_events: list[dict[str, Any]] = []
    latest_limits: dict[str, Any] | None = None
    latest_limits_ts: datetime | None = None

    for path in sorted(root.rglob("*.jsonl")):
      try:
        if datetime.fromtimestamp(path.stat().st_mtime, timezone.utc) < cutoff - timedelta(days=1):
            continue
      except OSError:
        continue

      stat = SessionStats(sid=path.stem, file=str(path))
      saw_recent = False

      try:
        lines = path.open(encoding="utf-8", errors="ignore")
      except OSError:
        continue

      with lines:
        for line in lines:
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue

            ts = parse_time(obj.get("timestamp"))
            payload = obj.get("payload") or {}
            top_type = obj.get("type")
            payload_type = payload.get("type")

            if ts and ts >= cutoff:
                saw_recent = True
                if not stat.started_at or ts < stat.started_at:
                    stat.started_at = ts
                if not stat.ended_at or ts > stat.ended_at:
                    stat.ended_at = ts

            if top_type == "session_meta":
                stat.sid = str(payload.get("id") or stat.sid)
                stat.cwd = payload.get("cwd") or stat.cwd
                meta_ts = parse_time(payload.get("timestamp"))
                if meta_ts and meta_ts >= cutoff:
                    stat.started_at = stat.started_at or meta_ts
                    saw_recent = True
                continue

            if top_type == "turn_context":
                if payload.get("model"):
                    stat.model = str(payload.get("model"))
                if payload.get("cwd"):
                    stat.cwd = payload.get("cwd")
                continue

            if payload_type == "token_count":
                limits = payload.get("rate_limits")
                if isinstance(limits, dict) and (latest_limits_ts is None or (ts and ts >= latest_limits_ts)):
                    latest_limits = limits
                    latest_limits_ts = ts or latest_limits_ts

                info = payload.get("info") or {}
                last_usage = usage_snapshot(info.get("last_token_usage"))
                total_usage = usage_snapshot(info.get("total_token_usage"))
                prev_total_usage = stat.prev_total_usage
                if total_usage:
                    stat.prev_total_usage = total_usage
                if ts and ts >= cutoff:
                    # token_count can repeat identical snapshots; count cumulative deltas, not every snapshot.
                    usage = usage_delta(total_usage, prev_total_usage) if total_usage and prev_total_usage else last_usage
                    if not usage:
                        continue
                    add_usage(stat.usage, usage)
                    stat.calls += 1
                    events.append({
                        "ts": ts,
                        "sid": stat.sid,
                        "usage": usage,
                        "model": stat.model,
                    })
                continue

            if payload_type == "task_complete" and ts and ts >= cutoff:
                stat.completions += 1
                duration = payload.get("duration_ms")
                if isinstance(duration, (int, float)) and math.isfinite(duration):
                    stat.duration_ms += int(duration)
                ttfb = payload.get("time_to_first_token_ms")
                if isinstance(ttfb, (int, float)) and math.isfinite(ttfb):
                    stat.ttfb_ms += int(ttfb)
                    stat.ttfb_count += 1
                    ttfb_events.append({"ts": ts, "sid": stat.sid, "model": stat.model, "ttfb_ms": int(ttfb)})
                continue

            if payload_type in {"error", "turn_aborted"} and ts and ts >= cutoff:
                stat.failures += 1
                failure_events.append({"ts": ts, "sid": stat.sid, "model": stat.model})

      if saw_recent and (stat.calls or stat.completions or stat.failures):
        sessions.append(stat)

    return sessions, events, latest_limits, ttfb_events, failure_events


def bucket_events(events: list[dict[str, Any]], now: datetime, minutes: int) -> list[dict[str, Any]]:
    bucket_count = 11
    end = now.replace(second=0, microsecond=0)
    start = end - timedelta(minutes=minutes)
    step = minutes / (bucket_count - 1)
    buckets = []
    for idx in range(bucket_count):
        b_start = start + timedelta(minutes=round(idx * step))
        b_end = start + timedelta(minutes=round((idx + 1) * step)) if idx < bucket_count - 1 else end + timedelta(seconds=1)
        buckets.append({"start": b_start, "end": b_end, **{key: 0 for key in USAGE_KEYS}})

    for event in events:
        ts = event["ts"]
        if ts < start or ts > end:
            continue
        idx = min(bucket_count - 1, max(0, int(((ts - start).total_seconds() / 60) / step)))
        add_usage(buckets[idx], event["usage"])

    return [
        {
            "label": display_time(bucket["start"]),
            "input": bucket["input_tokens"],
            "cached": bucket["cached_input_tokens"],
            "output": bucket["output_tokens"],
            "reasoning": bucket["reasoning_output_tokens"],
            "total": bucket["total_tokens"],
        }
        for bucket in buckets
    ]


def build_payload(root: Path, days: int, trend_minutes: int) -> dict[str, Any]:
    now = datetime.now(timezone.utc)
    cutoff = now - timedelta(days=days)
    sessions, events, limits, ttfb_events, failure_events = load_sessions(root, cutoff)
    events.sort(key=lambda event: event["ts"])
    ttfb_events.sort(key=lambda event: event["ts"])
    failure_events.sort(key=lambda event: event["ts"])

    totals = {key: 0 for key in USAGE_KEYS}
    for event in events:
        add_usage(totals, event["usage"])

    by_model: dict[str, dict[str, Any]] = defaultdict(lambda: {"tokens": 0, "requests": 0, "ttfb_ms": 0, "ttfb_count": 0})
    for session in sessions:
        model = session.model or "unknown"
        by_model[model]["tokens"] += session.usage["total_tokens"]
        by_model[model]["requests"] += session.calls
        by_model[model]["ttfb_ms"] += session.ttfb_ms
        by_model[model]["ttfb_count"] += session.ttfb_count

    session_rows = []
    for idx, session in enumerate(sorted(sessions, key=lambda s: s.usage["total_tokens"], reverse=True)[:20], 1):
        session_rows.append({
            "rank": idx,
            "name": project_name(session.cwd, f"session {session.sid[-6:]}"),
            "model": session.model,
            "tokens": session.usage["total_tokens"],
            "tokensLabel": fmt_int(session.usage["total_tokens"]),
            "requests": session.calls,
            "duration": fmt_duration(session.duration_ms / 1000 if session.duration_ms else None),
            "status": "ok" if session.failures == 0 else "warn",
        })

    max_session_tokens = max([row["tokens"] for row in session_rows] or [1])
    for row in session_rows:
        row["percent"] = round(row["tokens"] / max_session_tokens * 100)

    model_rows = []
    for model, data in sorted(by_model.items(), key=lambda item: item[1]["tokens"], reverse=True)[:12]:
        avg_latency = (data["ttfb_ms"] / 1000) / max(1, data["ttfb_count"]) if data["ttfb_ms"] else 0
        model_rows.append({
            "name": model,
            "tokens": data["tokens"],
            "tokensLabel": fmt_int(data["tokens"]),
            "requests": data["requests"],
            "latency": avg_latency,
            "latencyLabel": f"{avg_latency:.2f}s" if avg_latency else "--",
        })

    max_model_tokens = max([row["tokens"] for row in model_rows] or [1])
    for row in model_rows:
        row["percent"] = round(row["tokens"] / max_model_tokens * 100)

    trend = bucket_events(events, now, trend_minutes)
    trend_step_minutes = trend_minutes / max(1, len(trend) - 1)
    peak = max(trend, key=lambda row: row["total"], default={"total": 0, "label": "--"})

    primary = (limits or {}).get("primary") or {}
    secondary = (limits or {}).get("secondary") or {}
    primary_used = safe_percent(primary.get("used_percent"))
    secondary_used = safe_percent(secondary.get("used_percent"))
    primary_reset = datetime.fromtimestamp(primary["resets_at"], timezone.utc) if isinstance(primary.get("resets_at"), (int, float)) else None
    secondary_reset = datetime.fromtimestamp(secondary["resets_at"], timezone.utc) if isinstance(secondary.get("resets_at"), (int, float)) else None

    calls = sum(session.calls for session in sessions)
    failures = sum(session.failures for session in sessions)
    completions = sum(session.completions for session in sessions)
    success_rate = 100.0 if calls == 0 else max(0.0, min(100.0, (calls - failures) / calls * 100))
    failure_rate = 0.0 if calls == 0 else max(0.0, min(100.0, failures / calls * 100))
    cache_hit = 0.0 if totals["input_tokens"] == 0 else totals["cached_input_tokens"] / totals["input_tokens"] * 100
    session_catalog = {
        session.sid: {
            "name": project_name(session.cwd, f"session {session.sid[-6:]}"),
            "model": session.model,
        }
        for session in sessions
    }
    for event in events:
        session_catalog.setdefault(event["sid"], {"name": f"session {event['sid'][-6:]}", "model": event.get("model") or "unknown"})

    records = [
        [
            int(event["ts"].timestamp() * 1000),
            event["sid"],
            event.get("model") or "unknown",
            event["usage"]["input_tokens"],
            event["usage"]["cached_input_tokens"],
            event["usage"]["output_tokens"],
            event["usage"]["reasoning_output_tokens"],
            event["usage"]["total_tokens"],
        ]
        for event in events
    ]
    ttfb_records = [
        [int(event["ts"].timestamp() * 1000), event["sid"], event.get("model") or "unknown", event["ttfb_ms"]]
        for event in ttfb_events
    ]
    failure_records = [
        [int(event["ts"].timestamp() * 1000), event["sid"], event.get("model") or "unknown"]
        for event in failure_events
    ]

    return {
        "generatedAt": now.astimezone().strftime("%Y-%m-%d %H:%M:%S"),
        "windowDays": days,
        "availableRange": {
            "start": min((record[0] for record in records), default=int(cutoff.timestamp() * 1000)),
            "end": max((record[0] for record in records), default=int(now.timestamp() * 1000)),
        },
        "sessionsCatalog": session_catalog,
        "records": records,
        "ttfbRecords": ttfb_records,
        "failureRecords": failure_records,
        "summary": {
            "totalTokens": totals["total_tokens"],
            "totalTokensLabel": fmt_int(totals["total_tokens"]),
            "inputTokens": totals["input_tokens"],
            "inputLabel": fmt_int(totals["input_tokens"]),
            "cachedTokens": totals["cached_input_tokens"],
            "cachedLabel": fmt_int(totals["cached_input_tokens"]),
            "outputTokens": totals["output_tokens"],
            "outputLabel": fmt_int(totals["output_tokens"]),
            "reasoningTokens": totals["reasoning_output_tokens"],
            "reasoningLabel": fmt_int(totals["reasoning_output_tokens"]),
            "requests": calls,
            "requestsLabel": f"{calls:,}",
            "failures": failures,
            "successRate": success_rate,
            "successRateLabel": f"{success_rate:.1f}%",
            "cacheHit": cache_hit,
            "cacheHitLabel": f"{cache_hit:.1f}%",
            "peakTokens": peak["total"],
            "peakLabel": fmt_int(peak["total"]),
            "peakTime": peak["label"],
            "peakTpmLabel": f"{fmt_int(peak['total'] / max(1, trend_step_minutes))} TPM",
        },
        "limits": {
            "planType": (limits or {}).get("plan_type") or "unknown",
            "primaryUsed": primary_used,
            "primaryRemaining": None if primary_used is None else 100 - primary_used,
            "primaryReset": display_time(primary_reset),
            "primaryWindowMinutes": primary.get("window_minutes"),
            "secondaryUsed": secondary_used,
            "secondaryRemaining": None if secondary_used is None else 100 - secondary_used,
            "secondaryReset": display_time(secondary_reset),
            "secondaryWindowMinutes": secondary.get("window_minutes"),
            "rateLimitReachedType": (limits or {}).get("rate_limit_reached_type"),
        },
        "trend": trend,
        "sessions": session_rows,
        "models": model_rows,
        "risk": [
            {"name": "5h", "value": primary_used or 0, "label": f"{primary_used or 0:.0f}% used", "limit": "Codex primary"},
            {"name": "Week", "value": secondary_used or 0, "label": f"{secondary_used or 0:.0f}% used", "limit": "Codex secondary"},
            {"name": "Cache", "value": cache_hit, "label": f"{cache_hit:.0f}% hit", "limit": "local logs"},
            {"name": "Fail", "value": failure_rate, "label": f"{failures} ({failure_rate:.1f}%)", "limit": "errors"},
        ],
        "coverage": [
            {"metric": "真实额度", "source": "token_count.rate_limits", "status": "ok" if limits else "missing"},
            {"metric": "Token 消耗", "source": "token_count.last_token_usage", "status": "ok" if events else "missing"},
            {"metric": "会话排行", "source": "session_meta + token_count", "status": "ok" if sessions else "missing"},
            {"metric": "模型排行", "source": "turn_context.model", "status": "ok" if model_rows else "missing"},
            {"metric": "峰值速率", "source": "selected range buckets", "status": "ok" if events else "missing"},
            {"metric": "缓存命中", "source": "cached_input_tokens / input_tokens", "status": "ok" if totals["input_tokens"] else "missing"},
        ],
    }


def main() -> None:
    parser = argparse.ArgumentParser(description="Generate CodexScope Codex data.js")
    parser.add_argument("--root", default=str(Path.home() / ".codex" / "sessions"))
    parser.add_argument("--out", default=str(Path(__file__).with_name("data.js")))
    parser.add_argument("--days", type=int, default=30)
    parser.add_argument("--trend-minutes", type=int, default=300)
    args = parser.parse_args()

    payload = build_payload(Path(args.root).expanduser(), args.days, args.trend_minutes)
    out = Path(args.out)
    out.write_text(
        "window.CODEXSCOPE_DATA = "
        + json.dumps(payload, ensure_ascii=False, separators=(",", ":"))
        + ";\nwindow.QUOTASCOPE_DATA = window.CODEXSCOPE_DATA;\n",
        encoding="utf-8",
    )
    print(f"wrote {out} ({payload['summary']['requestsLabel']} requests, {payload['summary']['totalTokensLabel']} tokens)")


if __name__ == "__main__":
    main()
