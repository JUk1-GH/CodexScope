#!/bin/zsh
set -e

cd "$(dirname "$0")/.."

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 was not found. Please install Python 3 first."
  exit 1
fi

python3 generate_codex_data.py
open index.html
