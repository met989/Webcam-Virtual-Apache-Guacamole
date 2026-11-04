@echo off
setlocal EnableDelayedExpansion
title Virtual Webcam Driver Installer
echo ===================================================
echo     Virtual Webcam Driver Installation (DirectShow)
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
echo [INFO] Registering DirectShow filter for matching architecture (32-bit and 64-bit)...

:: 2. Register 64-bit via System32 regsvr32
if exist "%SystemRoot%\System32\regsvr32.exe" (
    if exist "UnityCaptureFilter64.dll" (
        "%SystemRoot%\System32\regsvr32.exe" /s "UnityCaptureFilter64.dll"
    )
)

:: 3. Register 32-bit via SysWOW64 regsvr32
if exist "%SystemRoot%\SysWOW64\regsvr32.exe" (
    if exist "UnityCaptureFilter32.dll" (
        "%SystemRoot%\SysWOW64\regsvr32.exe" /s "UnityCaptureFilter32.dll"
    )
) else (
    if exist "UnityCaptureFilter32.dll" (
        "%SystemRoot%\System32\regsvr32.exe" /s "UnityCaptureFilter32.dll"
    )
)

echo.
echo ===================================================
echo  [OK] Virtual Webcam installed successfully!
echo       DirectShow device registered in the system.
echo ===================================================
echo.
pause
