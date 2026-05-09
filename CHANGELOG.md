# Changelog

## v0.1.9 - 2026-05-09

- Switched generated exports to compact `recordsV2` rows and delta timestamps, with catalogs kept in the raw sidecar to reduce first-load `data.js` size.
- Added precomputed dashboard views for common ranges so preset filters avoid rescanning raw records in the browser.
- Exported model pricing rules from the generator so precomputed views and custom date ranges share one cost-estimation source.
- Rendered the cost popover price table from exported pricing rules instead of maintaining a separate static HTML table.
- Compact precomputed chart buckets to shrink first-load dashboard views without changing the UI.
- Split raw event rows into `data.raw.js`, loaded only when custom date ranges need raw records.
- Added a raw sidecar schema marker so hot-start freshness checks do not scan the full raw export.
- Fixed custom `--raw-out` paths by writing and validating the browser-relative sidecar path in `data.js`.
- Refactored preset view generation to aggregate summaries, charts, peak rate, sessions, and model stats in a single token-event pass.
- Date preset views now slice sorted runtime events by time range before aggregating, avoiding full-history scans for short ranges.
- Removed duplicate legacy top-level summary, trend, session, model, risk, and coverage exports from the main payload.
- Included failure-only and latency-only events when deriving available data bounds for generated exports.
- Clamped success and failure rates so sparse error-only logs cannot show failure percentages above 100%.
- Show missing quota data as waiting for `rate_limits` instead of rendering it like an exhausted 0% quota.
- Improved distribution chart value and x-axis label responsiveness across narrow dashboard widths.
- Kept distribution bar values accessible through titles and `aria-label`s while limiting visible labels to prevent clutter.
- Expanded responsive verification to wait for async custom-range rendering and assert accessible distribution values.
- Reused the already-loaded parsed cache during regeneration so runs with changed logs do not parse the large cache JSON twice.
- Bumped cache output stamping to regenerate once after the data-shape upgrade while still reusing readable parsed log cache.
- Slimmed release packages by removing bundled README docs from platform zips and stripping Go build ids.

## v0.1.8 - 2026-05-09

- Switched trend chart anchors to market-style adaptive time grains.
- Split trend buckets from distribution buckets so curves can be detailed while bars stay readable.
- Added the active trend granularity to the chart header, such as `10分钟/点`.

## v0.1.7 - 2026-05-09

- Deduplicated repeated `token_count` snapshots written into overlapping JSONL files.
- Fixed inflated peak TPM values caused by duplicate session logs.
- Bumped cache format so affected users regenerate clean local data once.
- Added a regression test for repeated snapshot deduplication.

## v0.1.6 - 2026-05-09

- Added a `历史` date filter for all exported local records.
- Changed the generator default to export all local history instead of only the last 30 days.
- Kept `--days N` available for users who want a bounded export window.
- Added tests for all-history cutoff and cache-window behavior.

## v0.1.5 - 2026-05-09

- Fixed quota selection when Codex logs contain both global Codex limits and model-specific limits.
- The dashboard now prefers the global `limit_id=codex` quota for the 5h window and weekly limit.
- Added quota source display in the UI.
- Added tests for global quota precedence.

## v0.1.4 - 2026-05-09

- Simplified release package layout for non-technical users.
- Release zips now show only the launcher, `START-HERE.txt`, and a `CodexScope Files` folder at the top level.
- Moved app files, binaries, and docs into clearer subfolders.

## v0.1.3 - 2026-05-09

- Improved macOS and Windows launchers.
- Added clearer first-run guidance for Gatekeeper and release zip usage.
- Reduced confusion between user-facing release packages and GitHub source archives.

## v0.1.2 - 2026-05-09

- Improved startup speed by skipping regeneration when `data.js` is already current.
- Preserved safe prebuilt launcher behavior in release zips.
- Kept source checkouts able to rebuild from Go when needed.

## v0.1.1 - 2026-05-09

- Kept older cache files usable after the cache format upgrade.
- Reused unchanged log cache entries instead of forcing a full rescan.
- Added prebuilt macOS arm64 and Windows amd64 packages.

## v0.1.0 - 2026-05-09

- Initial public release.
- Added local Codex token, quota, session, model, rate, and estimated cost dashboard.
- Added prebuilt packages for macOS arm64 and Windows amd64.
