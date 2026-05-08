# QuotaScope

QuotaScope is a local-first dashboard for inspecting Codex usage from local session logs. It turns local Codex metadata into a clean desktop dashboard with token trends, quota and risk status, session rankings, model rankings, request distribution, cache hit rate, and estimated cost.

![QuotaScope dashboard](assets/dashboard-24h.png)

The dashboard is a static HTML app: no backend, no account connection, and no hosted telemetry. Your real usage export stays local in `data.js`, which is intentionally ignored by git.

## Why

Codex usage is easiest to understand when quota, token volume, model mix, and session-level hotspots are visible in one place. QuotaScope is built for that narrow job: open a local page, generate a local export, and see where usage went without shipping prompts or project data to another service.

## Features

- Cumulative token trend with absolute and logarithmic views
- Date filters for last 24 hours, today, last 7 days, last 30 days, and custom ranges
- Request and token distribution charts for spotting usage peaks
- Codex quota and risk status from local `rate_limits` events when available
- Session and model rankings with token totals and request counts
- Estimated cost by model and token type, shown in USD by default with optional CNY conversion
- Local-only data generation from `~/.codex/sessions`
- Desktop-focused responsive layout with a lightweight static frontend

## Quick Start

Download the project and open `index.html` directly in a browser. It will show bundled sample data immediately, so you can preview the dashboard without running anything else.

To view your real local Codex usage, use the launcher for your system:

- **macOS**: double-click `macos/open-dashboard.command`
- **Windows**: double-click `windows/open-dashboard.cmd`

The launcher generates `data.js` from your local Codex logs and then opens `index.html`.

You can also run the same steps manually on macOS or Linux:

```bash
python3 generate_codex_data.py
open index.html
```

On Windows PowerShell:

```powershell
py .\generate_codex_data.py
start .\index.html
```

By default, the generator reads Codex logs from:

- macOS/Linux: `~/.codex/sessions`
- Windows: `%USERPROFILE%\.codex\sessions`

If your Codex sessions are stored elsewhere, pass the path explicitly:

```powershell
py .\generate_codex_data.py --root "$env:USERPROFILE\.codex\sessions"
```

The generator writes `data.js` next to `index.html`. Once that file exists, the dashboard automatically uses your real local data instead of the bundled demo. `data.js` may contain private project names, session ids, timestamps, usage patterns, and quota status, so it is excluded by `.gitignore`.

## What Gets Displayed

- **Token trend**: cumulative input, cached, output, and reasoning token usage over the selected range.
- **Quota and risk**: remaining short-window and weekly quota when Codex local logs include rate-limit metadata.
- **Distribution**: request count or token volume grouped by time bucket.
- **Rankings**: busiest sessions and models for the selected period.
- **Cost estimate**: a local estimate using token counts and the built-in model price table.

## Cost Estimates

The cost card is an estimate, not an official bill. It uses local token counts and a built-in table based on OpenAI's published USD model prices. Actual ChatGPT/Codex billing, credits, and subscription quota status should always be checked with the official account or billing page.

USD is the source currency. The CNY view is only a display conversion. When available, QuotaScope fetches the USD/CNY rate from the Frankfurter API with the ECB provider selected. If that request fails, it falls back to the last bundled reference rate and marks the conversion as offline fallback in the UI.

## Verify Layout

The responsive visual audit uses Playwright:

```bash
npm install
npm run verify
```

## Privacy

QuotaScope does not send data to a server. `generate_codex_data.py` reads local Codex session logs and exports only usage metadata:

- session id and working-directory basename
- model name
- token counts
- rate limit metadata
- task duration, first-token latency, failures

It does not export prompt text, assistant messages, tool output, or file contents.

Review `data.js` before sharing screenshots or artifacts generated from your own usage.

## License

MIT
