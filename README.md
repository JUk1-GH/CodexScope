# CodexScope

English | [简体中文](README.zh-CN.md)

[![LINUX DO](https://img.shields.io/badge/LINUX-DO-FFB003?style=flat-square)](https://linux.do)

CodexScope is a local-first dashboard for inspecting Codex usage from local session logs. It turns local Codex metadata into a clean desktop dashboard with token trends, quota and risk status, session rankings, model rankings, request distribution, cache hit rate, and estimated cost.

![CodexScope dashboard](assets/codexscope-dashboard-24h.png)

The dashboard is a static HTML app: no backend, no account connection, and no hosted telemetry. Your real usage export stays local in `data.js`, which is intentionally ignored by git.

## Why

Codex usage is easiest to understand when quota, token volume, model mix, and session-level hotspots are visible in one place. CodexScope is built for that narrow job: open a local page, generate a local export, and see where usage went without shipping prompts or project data to another service.

## Features

- Cumulative token trend with absolute and logarithmic views
- Date filters for last 24 hours, today, last 7 days, last 30 days, all history, and custom ranges
- Request and token distribution charts for spotting usage peaks
- Codex quota and risk status from local `rate_limits` events when available
- Session and model rankings with token totals and request counts
- Estimated cost by model and token type, shown in USD by default with optional CNY conversion
- Local-only data generation from `~/.codex/sessions`
- Desktop-focused responsive layout with a lightweight static frontend

## Quick Start

Download the project and open `index.html` directly in a browser. It will show bundled sample data immediately, so you can preview the dashboard without running anything else.

To view your real local Codex usage, normal users should download the platform package from GitHub Releases:

- **macOS**: download `CodexScope-mac.zip`, unzip it, then double-click `Open CodexScope.command` in the extracted folder
- **Windows**: download `CodexScope-windows.zip`, unzip it, then double-click `Open CodexScope.cmd` in the extracted folder

Release zips include a prebuilt generator, so normal users do not need to install Go. The launcher generates `data.js` from your local Codex logs and then opens `index.html`. Source checkouts can still fall back to `go build` when the prebuilt generator is absent.
Subsequent runs reuse a local `.codexscope-cache.json` file and only rescan changed session logs, so repeated launches should be much faster.

Note: GitHub's automatic **Source code (zip)** asset is for developers, not the recommended user download. It may require Go or local compilation. Prefer `CodexScope-mac.zip` / `CodexScope-windows.zip`.

If macOS says it cannot verify `open-dashboard.command`, open **System Settings → Privacy & Security**, find the blocked `open-dashboard.command` message, and click **Open Anyway**. You can also right-click the file and choose **Open**.

If macOS still refuses to open it, run this once in Terminal from the project folder:

```bash
xattr -dr com.apple.quarantine .
chmod +x macos/open-dashboard.command
```

You can also run the same steps manually on macOS or Linux:

```bash
go run generate_codex_data.go
open index.html
```

On Windows PowerShell:

```powershell
go run .\generate_codex_data.go
start .\index.html
```

By default, the generator reads Codex logs from:

- macOS/Linux: `~/.codex/sessions`
- Windows: `%USERPROFILE%\.codex\sessions`

If your Codex sessions are stored elsewhere, pass the path explicitly:

```powershell
go run .\generate_codex_data.go --root "$env:USERPROFILE\.codex\sessions"
```

The generator writes `data.js` next to `index.html`. Once that file exists, the dashboard automatically uses your real local data instead of the bundled demo. `data.js` and `.codexscope-cache.json` may contain private project names, session ids, timestamps, usage patterns, and quota status, so both are excluded by `.gitignore`.

## Project Structure

- `index.html`: the static dashboard shell.
- `styles.css`: dashboard layout and visual styling.
- `app.ts`: TypeScript source for charts, filters, rankings, quota display, and cost estimation.
- `app.js`: compiled browser script loaded by `index.html`.
- `generate_codex_data.go`: the local data generator. It scans Codex JSONL session logs, extracts usage metadata, and writes `data.js`.
- `data.sample.js`: bundled demo data used when no local `data.js` exists.
- `CHANGELOG.md`: release notes for each published version.
- `macos/open-dashboard.command`: macOS launcher that runs the generator and opens the dashboard.
- `windows/open-dashboard.cmd`: Windows launcher that runs the generator and opens the dashboard.
- `verify_responsive.js`: Playwright-based layout and interaction audit.
- `scripts/build-release.sh`: builds platform-specific release folders and zip packages.
- `assets/`: screenshots and static project assets.

## Data Flow

1. Codex writes local JSONL session logs under `~/.codex/sessions`.
2. `generate_codex_data.go` scans local `.jsonl` files and extracts only usage metadata: token counts, model names, session ids, timing, failures, and rate-limit metadata.
3. The generator writes those records to `data.js` as `window.CODEXSCOPE_DATA`.
4. `index.html` loads `data.sample.js` first and then `data.js`. If real local data exists, it overrides the sample data.
5. Date filters, charts, rankings, quota status, and cost estimates are computed in the browser from that local record set.

## What Gets Displayed

- **Token trend**: cumulative input, cached, output, and reasoning token usage over the selected range.
- **Quota and risk**: remaining short-window and weekly quota when Codex local logs include rate-limit metadata.
- **Distribution**: request count or token volume grouped by time bucket.
- **Rankings**: busiest sessions and models for the selected period.
- **Cost estimate**: a local estimate using token counts and the built-in model price table.

## Cost Estimates

The cost card is an estimate, not an official bill. It uses local token counts and a built-in table based on OpenAI's published USD model prices. Actual ChatGPT/Codex billing, credits, and subscription quota status should always be checked with the official account or billing page.

USD is the source currency. The CNY view is only a display conversion. When available, CodexScope fetches the USD/CNY rate from the Frankfurter API with the ECB provider selected. If that request fails, it falls back to the last bundled reference rate and marks the conversion as offline fallback in the UI.

## Verify Layout

The responsive visual audit uses Playwright:

```bash
npm install
npm run verify
```

## Build Release Packages

Release packages include prebuilt generators, so end users do not need Go:

```bash
npm install
npm run release:local
```

This writes:

- `dist/CodexScope-mac.zip` with a root-level launcher and instructions, with the rest tucked under `CodexScope Files/`
- `dist/CodexScope-windows.zip` with a root-level launcher and instructions, with the rest tucked under `CodexScope Files/`

The GitHub Actions release workflow builds the same zip files for tags named `v*`.

## Privacy

CodexScope does not send data to a server. `generate_codex_data.go` reads local Codex session logs and exports only usage metadata:

- session id and working-directory basename
- model name
- token counts
- rate limit metadata
- task duration, first-token latency, failures

It does not export prompt text, assistant messages, tool output, or file contents.

Review `data.js` before sharing screenshots or artifacts generated from your own usage.

## License

MIT
