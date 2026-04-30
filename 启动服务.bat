@echo off
chcp 65001 >nul 2>&1
title Leo-Go Server

:: Kill any existing leo-go process
taskkill /F /IM leo-go.exe >nul 2>&1

:: Wait briefly for port release
timeout /t 1 /nobreak >nul 2>&1

echo [Leo-Go] Starting server on port 8787 ...

:: Start server in background, then open browser
start "" "%~dp0leo-go.exe" -port 8787

:: Wait for server to be ready
timeout /t 2 /nobreak >nul 2>&1

echo [Leo-Go] Opening browser ...
start "" "http://127.0.0.1:8787/"

echo.
echo [Leo-Go] Server is running. Close this window to stop.
echo.

:: Keep window open and tail the server process
pause
