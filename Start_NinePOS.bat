@echo off
title Nine-POS Enterprise Server
echo =========================================
echo    Starting Nine-POS Enterprise Engine...
echo =========================================
echo.
echo Please do not close this window while using the POS.

:: Start the Go Server in a new window
start "" "NinePOS.exe"

:: Wait for 3 seconds to let the server and database boot up
timeout /t 3 /nobreak > NUL

:: Open the POS in the default web browser (Assuming your Go server runs on port 8080)
echo Opening Nine-POS in your browser...
start http://localhost:8080

exit