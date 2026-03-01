@echo off
title Nine-POS Enterprise Server
echo =========================================
echo    Starting Nine-POS Enterprise Engine...
echo =========================================
echo.
echo Please do not close this window while using the POS.

:: 1. Start the Go Server
start "" "NinePOS.exe"

:: 2. Wait for 3 seconds to let the server and database boot up
timeout /t 3 /nobreak > NUL

echo Opening Cashier Dashboard and Customer Display...

:: 3. Open Cashier View on the LEFT half of the screen
:: --app hides the browser UI. --window-position=X,Y sets the starting point. --window-size=W,H sets the size.
start chrome --app="http://localhost:8080/dashboard"

:: 4. Wait 1 second to ensure the first window opens smoothly
timeout /t 1 /nobreak > NUL

:: 5. Open Customer View on the RIGHT half of the screen (Starts at pixel 960)
start chrome --app="http://localhost:8080/customer-display"
exit