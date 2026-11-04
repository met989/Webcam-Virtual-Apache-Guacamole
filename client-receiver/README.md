# VirtualCam Receiver (Remote Workstation / Office PC)

High-performance video receiver in a single portable Windows executable (`webcam_receiver.exe`).

## Features:
1. Connects securely to the relay server (`wss://guacamole.yourdomain.com/cam/ws`).
2. Receives video stream and audio packets from your webcam sender.
3. Feeds frames directly in real time into the DirectShow **`Virtual Webcam`** device.
4. Any video conferencing application (Google Meet, Microsoft Teams, 3CX, Zoom, Chrome, Edge) sees the virtual webcam and microphone as native hardware devices.

---

## Usage:

### 1. Prerequisite:
Ensure the DirectShow virtual driver and VB-Audio virtual microphone are installed (handled automatically if deployed via GPO, or run `install_gpo.bat` / `driver\install_virtual_webcam.bat` as Administrator).

### 2. Launching the Receiver:
* Run **`webcam_receiver.exe`**.
* The icon appears in the system tray, indicating connection status to the relay.

### 3. Making Video Calls:
* Open Google Meet, Teams, or 3CX.
* In device settings, select:
  * Camera: **`Virtual Webcam`**
  * Microphone: **`CABLE Output (VB-Audio Virtual Cable)`** (or *Virtual Webcam Mic*)
* Your remote camera and audio stream appear with zero latency!

---

## Configuration (`config.json`):
Custom server or target room settings can be configured via `config.json` in `%LOCALAPPDATA%\VirtualWebcam` or in the application directory:
```json
{
  "relay_url": "wss://guacamole.yourdomain.com/cam/ws",
  "room": "auto",
  "token": ""
}
```
Environment variables are also supported:
* `VIRTUALCAM_RELAY_URL` (e.g. `wss://guacamole.yourdomain.com/cam/ws`)
* `VIRTUALCAM_ROOM` (e.g. `pc-office-01`)
* `VIRTUALCAM_TOKEN` (optional authentication token)
