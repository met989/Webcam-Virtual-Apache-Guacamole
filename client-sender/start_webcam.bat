@echo off
title VirtualCam Sender
echo ========================================================
echo   Launching VirtualCam Sender in Standalone App Mode
echo ========================================================
echo.

set "TARGET_URL=%~1"
if "%TARGET_URL%"=="" (
    if defined VIRTUALCAM_URL (
        set "TARGET_URL=%VIRTUALCAM_URL%"
    ) else (
        set "TARGET_URL=https://guacamole.yourdomain.com/cam"
    )
)

:: Launch the web interface in a dedicated native window (without browser chrome)
start msedge.exe --app="%TARGET_URL%" --window-size=1150,750

echo [OK] Webcam window launched. You can minimize it.
timeout /t 3 >nul
exit
