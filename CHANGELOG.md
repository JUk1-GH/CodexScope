# Changelog

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
