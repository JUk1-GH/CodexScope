#!/bin/zsh
set -e

cd "$(dirname "$0")"

if command -v python3 >/dev/null 2>&1; then
  python3 generate_codex_data.py
else
  echo "python3 was not found. Please install Python 3 first."
  exit 1
fi

open index.html
