@echo off
setlocal

cd /d "%~dp0.."

where py >nul 2>nul
if %errorlevel%==0 (
  py -3 generate_codex_data.py
  goto open_dashboard
)

where python >nul 2>nul
if %errorlevel%==0 (
  python generate_codex_data.py
  goto open_dashboard
)

echo Python 3 was not found. Please install Python 3 first.
pause
exit /b 1

:open_dashboard
if errorlevel 1 (
  echo Failed to generate data.js.
  pause
  exit /b 1
)

start "" "%cd%\index.html"
