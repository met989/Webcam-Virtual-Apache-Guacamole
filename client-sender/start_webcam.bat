@echo off
title VirtualCam Sender
echo ========================================================
echo   Launching VirtualCam Sender in Standalone App Mode
echo ========================================================
echo.

:: Launch the web interface in a dedicated native window (without browser chrome)
start msedge.exe --app="https://guacamole.reload.local/cam" --window-size=1150,750

echo [OK] Webcam window launched. You can minimize it.
timeout /t 3 >nul
exit
