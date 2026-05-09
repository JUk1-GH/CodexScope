#!/bin/zsh
set -e

cd "$(dirname "$0")"

dashboard_dir="$PWD"
generator="./codexscope-darwin-arm64"
cache_path=".codexscope-cache.json"
data_path="data.js"

if [ -f "CodexScope Files/app/index.html" ]; then
  dashboard_dir="$PWD/CodexScope Files/app"
  generator="./CodexScope Files/bin/codexscope-darwin-arm64"
  cache_path="$dashboard_dir/.codexscope-cache.json"
  data_path="$dashboard_dir/data.js"
elif [ -f "app/index.html" ]; then
  dashboard_dir="$PWD/app"
  generator="./bin/codexscope-darwin-arm64"
  cache_path="$dashboard_dir/.codexscope-cache.json"
  data_path="$dashboard_dir/data.js"
elif [ ! -f index.html ]; then
  cd ..
  dashboard_dir="$PWD"
fi

if [ -f "$generator" ]; then
  chmod +x "$generator" 2>/dev/null || true
fi

if [ -x "$generator" ] \
  && { [ ! -f generate_codex_data.go ] || { [ "$generator" -nt generate_codex_data.go ] && [ "$generator" -nt go.mod ] && [ "$generator" -nt go.sum ]; }; } \
  && "$generator" --out "$data_path" --cache "$cache_path"; then
  open "$dashboard_dir/index.html"
  exit 0
fi

if [ -x ./codexscope-generator ] \
  && [ ./codexscope-generator -nt generate_codex_data.go ] \
  && [ ./codexscope-generator -nt go.mod ] \
  && [ ./codexscope-generator -nt go.sum ] \
  && ./codexscope-generator --out "$data_path" --cache "$cache_path"; then
  open "$dashboard_dir/index.html"
  exit 0
fi

if command -v go >/dev/null 2>&1; then
  go build -trimpath -ldflags="-s -w" -o ./codexscope-generator generate_codex_data.go
  ./codexscope-generator --out "$data_path" --cache "$cache_path"
else
  echo "No prebuilt generator was found, and Go is not installed."
  echo "Please download CodexScope-mac.zip from the GitHub Releases page."
  exit 1
fi

open "$dashboard_dir/index.html"
