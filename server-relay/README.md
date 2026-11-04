# VirtualCam Relay - Standalone Debian / Linux Installation

This service runs as a native lightweight Linux binary managed by `systemd` or as a Docker container.

---

## Deployment Steps on Linux Host

### 1. Copy Executable to Server
Copy `virtualcam-relay-linux` to `/opt/virtual_webcam/` on Linux and make it executable:
```bash
sudo mkdir -p /opt/virtual_webcam/static
sudo cp virtualcam-relay-linux /opt/virtual_webcam/virtualcam-relay
sudo cp -r static/* /opt/virtual_webcam/static/
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
In your Nginx configuration, proxy `/cam/` to `http://127.0.0.1:9090/cam/` and `/cam/ws` to WebSocket (see `nginx-virtualcam.conf`).
Reload Nginx:
```bash
sudo nginx -s reload
# Or if running Nginx in Docker:
sudo docker exec guac-nginx nginx -s reload
```

---

## Testing & Verification
Open in your browser:
```text
https://<your-guacamole-host>/cam/
```
The **VirtualCam Sender** interface will load ready for use.
Check status and diagnostics at:
```text
https://<your-guacamole-host>/cam/status
```
