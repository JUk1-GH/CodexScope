@echo off
setlocal

cd /d "%~dp0.."

where go >nul 2>nul
if %errorlevel%==0 (
  go build -o codexscope-generator.exe generate_codex_data.go
  if errorlevel 1 goto generator_failed
  "%cd%\codexscope-generator.exe"
  goto open_dashboard
)

if exist "%cd%\codexscope-generator.exe" (
  "%cd%\codexscope-generator.exe"
  goto open_dashboard
)

echo Go was not found. Please install Go first.
pause
exit /b 1

:open_dashboard
if errorlevel 1 goto generator_failed
start "" "%cd%\index.html"
exit /b 0

:generator_failed
echo Failed to generate data.js.
pause
exit /b 1
