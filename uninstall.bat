@echo off
setlocal EnableDelayedExpansion
title Virtual Webcam - Complete Uninstaller
echo =========================================================================
echo   VirtualWebcam - Complete Removal ^& Registry/Startup Cleanup
echo =========================================================================
echo.

:: 1. Verify and elevate automatically to Administrator privileges
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [INFO] Requesting Administrator privileges (UAC)...
    powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
    exit /b
)

echo [1/6] Terminating Virtual Webcam processes...
taskkill /f /im webcam_receiver.exe >nul 2>&1
taskkill /f /im webcam_audio_service.exe >nul 2>&1
taskkill /f /im webcam_sender.exe >nul 2>&1
taskkill /f /im WebcamSender.exe >nul 2>&1

echo [2/6] Removing scheduled tasks...
schtasks /delete /tn "VirtualCamAudioService" /f >nul 2>&1
schtasks /delete /tn "WebcamAudioService" /f >nul 2>&1
schtasks /delete /tn "RunVirtualCam" /f >nul 2>&1
schtasks /delete /tn "VirtualCam" /f >nul 2>&1
schtasks /delete /tn "VirtualWebcam" /f >nul 2>&1

echo [3/6] Unregistering DirectShow Video Driver...
set "TARGET_DIR=C:\Program Files\VirtualWebcam"
if exist "%TARGET_DIR%\driver\UnityCaptureFilter64.dll" (
    "%SystemRoot%\System32\regsvr32.exe" /u /s "%TARGET_DIR%\driver\UnityCaptureFilter64.dll" >nul 2>&1
)
if exist "%TARGET_DIR%\driver\UnityCaptureFilter32.dll" (
    if exist "%SystemRoot%\SysWOW64\regsvr32.exe" (
        "%SystemRoot%\SysWOW64\regsvr32.exe" /u /s "%TARGET_DIR%\driver\UnityCaptureFilter32.dll" >nul 2>&1
    ) else (
        "%SystemRoot%\System32\regsvr32.exe" /u /s "%TARGET_DIR%\driver\UnityCaptureFilter32.dll" >nul 2>&1
    )
)

echo [4/6] Deep cleaning startup keys across ALL user profiles...
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Get-ChildItem Registry::HKEY_USERS | ForEach-Object { $sid = $_.PSChildName; if ($sid -notmatch '_Classes') { @('Run','Explorer\StartupApproved\Run','Explorer\StartupApproved\StartupFolder','Explorer\StartupApproved\StartupApp') | ForEach-Object { $p = \"Registry::HKEY_USERS\$sid\Software\Microsoft\Windows\CurrentVersion\$_\"; if (Test-Path $p) { (Get-ItemProperty $p).PSObject.Properties | Where-Object { $_.Name -match 'webcam|virtual' } | ForEach-Object { Remove-ItemProperty -Path $p -Name $_.Name -Force -ErrorAction SilentlyContinue } } } } }" >nul 2>&1

reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run" /v "VirtualWebcamReceiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run" /v "webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run" /v "Webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Run" /v "VirtualWebcamReceiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Run" /v "webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Run" /v "Webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run" /v "webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run" /v "Webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run" /v "VirtualWebcamReceiver" /f >nul 2>&1

powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Get-ChildItem -Path 'C:\Users\*\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\*webcam*.lnk' -Force -ErrorAction SilentlyContinue | Remove-Item -Force" >nul 2>&1
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Get-ChildItem -Path 'C:\ProgramData\Microsoft\Windows\Start Menu\Programs\Startup\*webcam*.lnk' -Force -ErrorAction SilentlyContinue | Remove-Item -Force" >nul 2>&1

echo [5/6] Removing local installation folders...
timeout /t 1 >nul
if exist "%TARGET_DIR%" (
    rd /s /q "%TARGET_DIR%" >nul 2>&1
)
if exist "C:\VirtualWebcamDriver" (
    rd /s /q "C:\VirtualWebcamDriver" >nul 2>&1
)

echo [6/6] Cleanup complete!
echo.
echo =========================================================================
echo  [OK] All components, startup keys, and files have been removed.
echo       NOTE: Close and reopen Task Manager to refresh the startup list!
echo =========================================================================
echo.
pause
