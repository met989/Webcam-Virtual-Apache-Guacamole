@echo off
setlocal EnableDelayedExpansion

:: =========================================================================
:: VirtualWebcam - Centralized GPO Installation & Update Script
:: Run via Computer Configuration -> Windows Settings -> Scripts (Startup)
:: =========================================================================

set "SOURCE_DIR=%~dp0"
if "%SOURCE_DIR:~-1%"=="\" set "SOURCE_DIR=%SOURCE_DIR:~0,-1%"

set "TARGET_DIR=C:\Program Files\VirtualWebcam"

:: 1. Create target directories
if not exist "%TARGET_DIR%" mkdir "%TARGET_DIR%"
if not exist "%TARGET_DIR%\driver" mkdir "%TARGET_DIR%\driver"
if not exist "%TARGET_DIR%\vbcable" mkdir "%TARGET_DIR%\vbcable"

:: 2. Stop running processes to allow file updates without locking
taskkill /f /im webcam_audio_service.exe >nul 2>&1
taskkill /f /im webcam_receiver.exe >nul 2>&1

:: 2b. Cleanup legacy entries and duplicates in Task Scheduler and Task Manager (StartupApproved)
schtasks /delete /tn "RunVirtualCam" /f >nul 2>&1
schtasks /delete /tn "VirtualCam" /f >nul 2>&1
schtasks /delete /tn "VirtualWebcam" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run" /v "webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run" /v "Webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Run" /v "webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Run" /v "Webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run" /v "webcam_receiver" /f >nul 2>&1
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run" /v "Webcam_receiver" /f >nul 2>&1
del /f /q "%ProgramData%\Microsoft\Windows\Start Menu\Programs\Startup\*webcam*.lnk" >nul 2>&1

:: 3. Smart file sync (only changed or missing files)
robocopy "%SOURCE_DIR%" "%TARGET_DIR%" webcam_receiver.exe webcam_audio_service.exe config.json /xo /r:1 /w:1 /njh /njs /nc /ns /np >nul
if exist "%SOURCE_DIR%\driver" (
    robocopy "%SOURCE_DIR%\driver" "%TARGET_DIR%\driver" UnityCaptureFilter64.dll UnityCaptureFilter32.dll /xo /r:1 /w:1 /njh /njs /nc /ns /np >nul
)
if exist "%SOURCE_DIR%\vbcable" (
    robocopy "%SOURCE_DIR%\vbcable" "%TARGET_DIR%\vbcable" /e /xo /r:1 /w:1 /njh /njs /nc /ns /np >nul
)

:: 3b. Unblock files (remove Mark of the Web Zone.Identifier to prevent security prompts)
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Get-ChildItem -Path '%TARGET_DIR%' -Recurse | Unblock-File" >nul 2>&1

:: 4. Register DirectShow Video Filter (Virtual Webcam) for matching architecture
if exist "%SystemRoot%\System32\regsvr32.exe" (
    if exist "%TARGET_DIR%\driver\UnityCaptureFilter64.dll" (
        "%SystemRoot%\System32\regsvr32.exe" /s "%TARGET_DIR%\driver\UnityCaptureFilter64.dll"
    )
)
if exist "%SystemRoot%\SysWOW64\regsvr32.exe" (
    if exist "%TARGET_DIR%\driver\UnityCaptureFilter32.dll" (
        "%SystemRoot%\SysWOW64\regsvr32.exe" /s "%TARGET_DIR%\driver\UnityCaptureFilter32.dll"
    )
) else (
    if exist "%TARGET_DIR%\driver\UnityCaptureFilter32.dll" (
        "%SystemRoot%\System32\regsvr32.exe" /s "%TARGET_DIR%\driver\UnityCaptureFilter32.dll"
    )
)

:: 5. Install Virtual Microphone Driver (VB-Audio Cable)
if exist "%TARGET_DIR%\vbcable\vbMmeCable64_win10.inf" (
    pnputil.exe /add-driver "%TARGET_DIR%\vbcable\vbMmeCable64_win10.inf" /install >nul 2>&1
)

:: 6. Autostart Video Receiver for all users on Logon (HKLM Run)
reg add "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run" /v "VirtualWebcamReceiver" /t REG_SZ /d "\"%TARGET_DIR%\webcam_receiver.exe\"" /f >nul 2>&1

:: 6b. Disable security popups for trusted local system binaries
reg add "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\Attachments" /v "SaveZoneInformation" /t REG_DWORD /d 1 /f >nul 2>&1
reg add "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\Associations" /v "LowRiskFileTypes" /t REG_SZ /d ".exe;.bat;.dll;" /f >nul 2>&1

:: 7. Virtual Microphone Background Service at System Startup (Scheduled Task SYSTEM)
schtasks /create /tn "VirtualCamAudioService" /tr "\"%TARGET_DIR%\webcam_audio_service.exe\"" /sc ONSTART /ru "SYSTEM" /f >nul 2>&1
schtasks /run /tn "VirtualCamAudioService" >nul 2>&1

exit /b 0
