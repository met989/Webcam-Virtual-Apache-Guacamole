@echo off
title Restore Webcam Driver
echo ========================================================
echo   Restore Webcam Driver (Remove WinUSB)
echo ========================================================
echo.

:: Automatic elevation to Administrator privileges
>nul 2>&1 "%SYSTEMROOT%\system32\cacls.exe" "%SYSTEMROOT%\system32\config\system"
if '%errorlevel%' NEQ '0' (
    echo Requesting Administrator privileges...
    powershell -Command "Start-Process cmd -ArgumentList '/c \"\"%~f0\"\"' -Verb runas"
    exit /b
)

echo [1/2] Removing WinUSB driver association...
pnputil /remove-device "USB\VID_32E6&PID_9221\202406181530"

echo [2/2] Scanning for hardware changes (restoring default Windows video driver)...
pnputil /scan-devices

echo.
echo ========================================================
echo  [OK] Driver restoration completed!
echo       Restart webcam_sender.exe to select your webcam.
echo ========================================================
echo.
pause
