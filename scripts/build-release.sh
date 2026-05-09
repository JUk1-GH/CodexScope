#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="$ROOT_DIR/dist"
MAC_DIR="$DIST_DIR/CodexScope-mac"
WIN_DIR="$DIST_DIR/CodexScope-windows"
MAC_FILES_DIR="$MAC_DIR/CodexScope Files"
WIN_FILES_DIR="$WIN_DIR/CodexScope Files"

cd "$ROOT_DIR"

npm run build:frontend

rm -rf "$DIST_DIR"
mkdir -p "$MAC_FILES_DIR/app" "$MAC_FILES_DIR/bin" "$MAC_FILES_DIR/docs" "$WIN_FILES_DIR/app" "$WIN_FILES_DIR/bin" "$WIN_FILES_DIR/docs"

app_files=(
  "index.html"
  "styles.css"
  "app.js"
  "data.sample.js"
)

for file in "${app_files[@]}"; do
  cp "$file" "$MAC_FILES_DIR/app/$file"
  cp "$file" "$WIN_FILES_DIR/app/$file"
done

for file in "README.md" "README.zh-CN.md" "LICENSE"; do
  cp "$file" "$MAC_FILES_DIR/docs/$file"
  cp "$file" "$WIN_FILES_DIR/docs/$file"
done

printf 'window.CODEXSCOPE_DATA = window.CODEXSCOPE_DATA || null;\n' > "$MAC_FILES_DIR/app/data.js"
printf 'window.CODEXSCOPE_DATA = window.CODEXSCOPE_DATA || null;\n' > "$WIN_FILES_DIR/app/data.js"

cp "macos/open-dashboard.command" "$MAC_DIR/Open CodexScope.command"
cp "windows/open-dashboard.cmd" "$WIN_DIR/Open CodexScope.cmd"

cat > "$MAC_DIR/START-HERE.txt" <<'TXT'
CodexScope macOS 用户先看

1. 双击 Open CodexScope.command。
2. 如果 macOS 拦截，打开 系统设置 > 隐私与安全性，点击 仍要打开。
3. CodexScope Files 文件夹不用点，程序会自动使用里面的文件。
4. 这个包已经内置编译好的程序，不需要安装 Go。

如果你下载的是 GitHub 自动生成的 Source code (zip)，那是给开发者看的源码包，不是普通用户推荐下载。

1. Double-click Open CodexScope.command.
2. If macOS blocks it, open System Settings > Privacy & Security, then click Open Anyway.
3. You do not need to open CodexScope Files manually.
4. This package already includes the compiled generator. You do not need Go.
TXT

cat > "$WIN_DIR/START-HERE.txt" <<'TXT'
CodexScope Windows 用户先看

1. 双击 Open CodexScope.cmd。
2. CodexScope Files 文件夹不用点，程序会自动使用里面的文件。
3. 这个包已经内置编译好的程序，不需要安装 Go。

如果你下载的是 GitHub 自动生成的 Source code (zip)，那是给开发者看的源码包，不是普通用户推荐下载。

1. Double-click Open CodexScope.cmd.
2. You do not need to open CodexScope Files manually.
3. This package already includes the compiled generator. You do not need Go.
TXT

GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o "$MAC_FILES_DIR/bin/codexscope-darwin-arm64" generate_codex_data.go
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$WIN_FILES_DIR/bin/codexscope-windows-amd64.exe" generate_codex_data.go

chmod +x "$MAC_DIR/Open CodexScope.command" "$MAC_FILES_DIR/bin/codexscope-darwin-arm64"

(
  cd "$DIST_DIR"
  zip -qr "CodexScope-mac.zip" "CodexScope-mac"
  zip -qr "CodexScope-windows.zip" "CodexScope-windows"
)

printf 'Built release packages:\n  %s\n  %s\n' \
  "$DIST_DIR/CodexScope-mac.zip" \
  "$DIST_DIR/CodexScope-windows.zip"
