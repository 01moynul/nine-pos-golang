@echo off
title Nine-POS Enterprise Server
echo =========================================
echo    STOPPING Nine-POS Enterprise Engine...
echo =========================================
echo.

echo Cleaning up old sessions..
:: 0. Close existing POS server and ALL Edge windows
taskkill /F /IM "NinePOS.exe" /T > NUL 2>&1
taskkill /F /IM "msedge.exe" /T > NUL 2>&1

:: Wait 1 second to ensure everything is fully closed
timeout /t 1 /nobreak > NUL

echo Thank you for Using NinePOS, see you in next work session.

exit