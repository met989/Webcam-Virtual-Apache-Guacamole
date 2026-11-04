(function() {
    function detectRoom() {
        var title = document.title || "";
        // 1. Connection title with FQDN (e.g. web03.domain.local -> web03)
        var match = title.match(/\b([a-zA-Z0-9_\-]+)\.[a-zA-Z0-9_\-\.]+\b/i);
        if (match && match[1]) {
            return match[1].toLowerCase();
        }
        // 2. Standard Guacamole title format: "<Connection Name> - Apache Guacamole"
        var mGuac = title.match(/^(.+?)\s*[-–—|]\s*Apache Guacamole/i);
        if (mGuac && mGuac[1]) {
            return mGuac[1].trim().toLowerCase();
        }
        // 3. Common workstation patterns (e.g. web03, pc01, client02)
        var m2 = title.match(/\b([a-zA-Z0-9_\-]+[0-9]+)\b/i);
        if (m2 && m2[1]) {
            return m2[1].toLowerCase();
        }
        return "";
    }

    function createOrUpdateButton() {
        var btn = document.getElementById('guac-cam-btn');
        var room = detectRoom();
        var targetUrl = room ? ('/cam/?room=' + encodeURIComponent(room)) : '/cam/';

        if (!btn) {
            btn = document.createElement('a');
            btn.id = 'guac-cam-btn';
            btn.target = '_blank';
            btn.title = 'Webcam (VirtualCam)';
            btn.innerHTML = '<span class="cam-icon">📹</span>';
            
            btn.addEventListener('click', function(e) {
                var currentRoom = detectRoom();
                btn.href = currentRoom ? ('/cam/?room=' + encodeURIComponent(currentRoom)) : '/cam/';
            });

            document.body.appendChild(btn);
        } else {
            btn.href = targetUrl;
            if (btn.querySelector('.cam-text')) {
                btn.innerHTML = '<span class="cam-icon">📹</span>';
            }
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', createOrUpdateButton);
    } else {
        createOrUpdateButton();
    }

    setInterval(createOrUpdateButton, 1500);
})();
