@echo off
setlocal

cd /d "%~dp0"
set "DASHBOARD_DIR=%cd%"
set "GENERATOR=%cd%\codexscope-windows-amd64.exe"
set "DATA_PATH=%cd%\data.js"
set "CACHE_PATH=%cd%\.codexscope-cache.json"

if exist "%cd%\CodexScope Files\app\index.html" (
  set "DASHBOARD_DIR=%cd%\CodexScope Files\app"
  set "GENERATOR=%cd%\CodexScope Files\bin\codexscope-windows-amd64.exe"
  set "DATA_PATH=%cd%\CodexScope Files\app\data.js"
  set "CACHE_PATH=%cd%\CodexScope Files\app\.codexscope-cache.json"
) else if exist "%cd%\app\index.html" (
  set "DASHBOARD_DIR=%cd%\app"
  set "GENERATOR=%cd%\bin\codexscope-windows-amd64.exe"
  set "DATA_PATH=%cd%\app\data.js"
  set "CACHE_PATH=%cd%\app\.codexscope-cache.json"
) else (
  if not exist "%cd%\index.html" cd /d "%~dp0.."
)

if exist "%GENERATOR%" (
  if not exist "%cd%\generate_codex_data.go" (
    "%GENERATOR%" --out "%DATA_PATH%" --cache "%CACHE_PATH%"
    goto open_dashboard
  )
)

where go >nul 2>nul
if %errorlevel%==0 (
  go build -trimpath -ldflags "-s -w" -o codexscope-generator.exe generate_codex_data.go
  if errorlevel 1 goto generator_failed
  "%cd%\codexscope-generator.exe" --out "%DATA_PATH%" --cache "%CACHE_PATH%"
  goto open_dashboard
)

echo No prebuilt generator was found, and Go is not installed.
echo Please download CodexScope-windows.zip from the GitHub Releases page.
pause
exit /b 1

:open_dashboard
if errorlevel 1 goto generator_failed
start "" "%DASHBOARD_DIR%\index.html"
exit /b 0

:generator_failed
echo Failed to generate data.js.
pause
exit /b 1
