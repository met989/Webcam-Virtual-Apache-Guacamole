@echo off
title Restore Webcam Driver
echo ========================================================
echo   Restore Webcam Driver (Remove WinUSB association)
echo ========================================================
echo.

:: Automatic elevation to Administrator privileges
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [INFO] Requesting Administrator privileges (UAC)...
    powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
    exit /b
)

echo [1/2] Removing WinUSB driver associations on video devices...
powershell -NoProfile -Command "Get-PnpDevice | Where-Object { ($_.FriendlyName -match 'webcam|camera' -or $_.Class -eq 'Camera' -or $_.Class -eq 'Image') -and $_.Service -match 'WinUSB' } | ForEach-Object { pnputil /remove-device $_.InstanceId }" >nul 2>&1
pnputil /remove-device "USB\VID_32E6&PID_9221\202406181530" >nul 2>&1

echo [2/2] Scanning for hardware changes (restoring default Windows video driver)...
pnputil /scan-devices

echo.
echo ========================================================
echo  [OK] Driver restoration completed!
echo       Restart webcam_sender.exe to select your webcam.
echo ========================================================
echo.
pause
