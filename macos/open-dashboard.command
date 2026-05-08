#!/bin/zsh
set -e

cd "$(dirname "$0")/.."

if [ -x ./codexscope-generator ] \
  && [ ./codexscope-generator -nt generate_codex_data.go ] \
  && [ ./codexscope-generator -nt go.mod ] \
  && [ ./codexscope-generator -nt go.sum ] \
  && ./codexscope-generator; then
  open index.html
  exit 0
fi

if command -v go >/dev/null 2>&1; then
  go build -o ./codexscope-generator generate_codex_data.go
  ./codexscope-generator
elif [ -x ./codexscope-generator ]; then
  ./codexscope-generator
else
  echo "Go was not found. Please install Go first."
  exit 1
fi

open index.html
