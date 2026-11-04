@echo off
setlocal EnableDelayedExpansion
title Virtual Webcam Driver Uninstaller
echo ===================================================
echo   Virtual Webcam Driver Removal (DirectShow)
echo ===================================================
echo.

:: 1. Verify and elevate automatically to Administrator privileges
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [INFO] Requesting Administrator privileges (UAC)...
    powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
    exit /b
)

cd /d "%~dp0"
echo [INFO] Unregistering DirectShow components...

if exist "%SystemRoot%\System32\regsvr32.exe" (
    if exist "UnityCaptureFilter64.dll" (
        "%SystemRoot%\System32\regsvr32.exe" /u /s "UnityCaptureFilter64.dll"
    )
)

if exist "%SystemRoot%\SysWOW64\regsvr32.exe" (
    if exist "UnityCaptureFilter32.dll" (
        "%SystemRoot%\SysWOW64\regsvr32.exe" /u /s "UnityCaptureFilter32.dll"
    )
) else (
    if exist "UnityCaptureFilter32.dll" (
        "%SystemRoot%\System32\regsvr32.exe" /u /s "UnityCaptureFilter32.dll"
    )
)

echo.
echo ===================================================
echo  [OK] Virtual Webcam removed successfully!
echo ===================================================
echo.
pause
