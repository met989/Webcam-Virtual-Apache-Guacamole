# VirtualCam Relay - Standalone Debian Installation

This service runs as a native lightweight Linux binary managed by `systemd`.

---

## Deployment Steps on Debian (`srvgua00`)

### 1. Copy Executable to Server
Copy `virtualcam-relay-linux` to `/opt/virtual_webcam/` on Debian and make it executable:
```bash
sudo mkdir -p /opt/virtual_webcam
sudo mv /tmp/virtualcam-relay-linux /opt/virtual_webcam/virtualcam-relay
sudo chmod +x /opt/virtual_webcam/virtualcam-relay
```

### 2. Enable and Start Systemd Service
Copy `virtualcam-relay.service` to systemd services and start it:
```bash
sudo cp virtualcam-relay.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now virtualcam-relay
```

To verify service status:
```bash
sudo systemctl status virtualcam-relay
```

### 3. Nginx Reverse Proxy
In your Nginx configuration, proxy `/cam/` to `http://127.0.0.1:9090/cam/` and `/cam/ws` to WebSocket.
Reload Nginx:
```bash
sudo docker exec guac-nginx nginx -s reload
```

---

## Testing & Verification
Open in your browser:
```text
https://guacamole.reload.local/cam/
```
The **VirtualCam Sender** interface will load ready for use.
