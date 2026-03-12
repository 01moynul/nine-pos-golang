@echo off
title Nine-POS Security Camera Engine
echo =========================================
echo    Starting Nine-POS Webcam Engine...
echo =========================================
echo.

echo Cleaning up old video sessions...
:: 1. Force close any existing Edge windows (The Kill Switch)
taskkill /F /IM "msedge.exe" /T > NUL 2>&1

:: 2. Wait 1 second to ensure Edge is fully closed
timeout /t 1 /nobreak > NUL

echo Opening Google Meet in Edge...

:: 3. Launch Edge with flags to prevent the "Restore Pages" popup after a force-close
start msedge --disable-session-crashed-bubble --disable-infobars "https://meet.google.com/zty-dbww-dwo"

exit