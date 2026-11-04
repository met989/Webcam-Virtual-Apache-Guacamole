# Centralized GPO Deployment Guide (Active Directory)

This package allows you to automatically and silently install **Virtual Webcam** (DirectShow Video) and **Virtual Webcam Mic** (VB-Cable Virtual Microphone) across all domain workstations via a single GPO startup policy from the Domain Controller.

---

## 1. Execution Architecture

* **Video Component (`webcam_receiver.exe`):**  
  Runs in the **interactive user session** (any user logging in via RDP, Guacamole, or locally), allowing Google Meet, Teams, Edge, and Chrome to access the DirectShow shared memory with zero latency.  
  Registered in the system registry: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run`.

* **Microphone Component (`webcam_audio_service.exe`):**  
  Runs as a **SYSTEM background service** at computer startup. Injects 16kHz PCM audio coming from the sender PC directly into the VB-Audio `CABLE Input` device, making it available to meeting apps as `CABLE Output` (*Virtual Webcam Mic*).

* **System Drivers (`regsvr32` + `pnputil`):**  
  Automatically registered at system startup with `SYSTEM` privileges through the GPO Startup Script.

---

## 2. Configuring GPO on the Domain Controller

### Step A: File Share
1. Copy the `deploy_gpo` folder to a network share readable by all domain computers (e.g. `\\domain.local\NETLOGON\deploy_gpo` or `\\Serverdc02\netlogon\deploy_gpo`).
2. Verify that `Domain Computers` (or `Authenticated Users`) have **Read & Execute** permissions.

### Step B: Create Group Policy Object
1. Open **Group Policy Management** (`gpmc.msc`) on your Domain Controller.
2. Create a new GPO (e.g. `Deploy-VirtualWebcam`) and link it to the target OU containing workstations (e.g., `OU=Workstations`).
3. Right-click the GPO and choose **Edit**.

### Step C: Configure Computer Startup Script
1. Navigate to:  
   `Computer Configuration` > `Policies` > `Windows Settings` > `Scripts (Startup/Shutdown)` > **`Startup`**.
2. Click **Add...**
3. In **Script Name**, enter the full UNC path to `install_gpo.bat`, e.g.:  
   `\\Serverdc02\netlogon\deploy_gpo\install_gpo.bat`
4. Leave script parameters empty and confirm with **OK**.

---

## 3. What the Script Performs on Client Startup
1. Syncs files locally into `C:\Program Files\VirtualWebcam\`.
2. Registers 64-bit and 32-bit DirectShow video filters (`regsvr32 /s UnityCaptureFilter*.dll`).
3. Installs the VB-Audio Cable virtual microphone driver into the Windows DriverStore (`pnputil /add-driver ... /install`).
4. Creates and starts the scheduled task `VirtualCamAudioService` (runs at system startup as `SYSTEM`).
5. Configures `webcam_receiver.exe` in `HKLM\...\Run` for all user logons.
6. Automatically unblocks files (`Zone.Identifier`) to prevent untrusted publisher security prompts.

---

## 4. Future Updates
To roll out an update (e.g., a new binary version or modified `config.json`):
1. Overwrite the files in the network share on the Domain Controller.
2. On next client reboot, `robocopy /xo` detects the newer files and synchronizes them automatically without manual intervention.
