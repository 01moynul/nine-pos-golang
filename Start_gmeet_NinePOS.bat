@echo off
title Nine-POS Security Camera Engine
echo =========================================
echo    Starting Nine-POS Webcam Engine...
echo =========================================
echo.

echo Cleaning up old video sessions...
:: 1. Force close any existing Chrome windows (The Kill Switch)
taskkill /F /IM "chrome.exe" /T > NUL 2>&1

:: 2. Wait 1 second to ensure Chrome is fully closed
timeout /t 1 /nobreak > NUL

echo Opening Google Meet in Chrome...

:: 3. Launch Chrome with flags to prevent the "Restore Pages" popup after a force-close
start chrome --disable-session-crashed-bubble --disable-infobars "https://meet.google.com/zty-dbww-dwo"

exit