package main

import (
	"crypto/tls"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
)

var (
	winmm              = syscall.NewLazyDLL("winmm.dll")
	procWaveOutOpen    = winmm.NewProc("waveOutOpen")
	procWaveOutClose   = winmm.NewProc("waveOutClose")
	procWaveOutPrepare = winmm.NewProc("waveOutPrepareHeader")
	procWaveOutUnprep  = winmm.NewProc("waveOutUnprepareHeader")
	procWaveOutWrite   = winmm.NewProc("waveOutWrite")
	procWaveOutGetNum  = winmm.NewProc("waveOutGetNumDevs")
	procWaveOutGetCaps = winmm.NewProc("waveOutGetDevCapsW")
)

type WAVEFORMATEX struct {
	wFormatTag      uint16
	nChannels       uint16
	nSamplesPerSec  uint32
	nAvgBytesPerSec uint32
	nBlockAlign     uint16
	wBitsPerSample  uint16
	cbSize          uint16
}

type WAVEHDR struct {
	lpData          uintptr
	dwBufferLength  uint32
	dwBytesRecorded uint32
	dwUser          uintptr
	dwFlags         uint32
	dwLoops         uint32
	lpNext          uintptr
	reserved        uintptr
}

type WAVEOUTCAPSW struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [32]uint16
	dwFormats      uint32
	wChannels      uint16
	wReserved1     uint16
	dwSupport      uint32
}

type WavePlayer struct {
	hWaveOut syscall.Handle
	opened   bool
	mu       sync.Mutex
}

func findCableInputDeviceID() uint32 {
	numDevs, _, _ := procWaveOutGetNum.Call()
	targetID := uint32(0xFFFFFFFF)
	for i := uint32(0); i < uint32(numDevs); i++ {
		var caps WAVEOUTCAPSW
		r, _, _ := procWaveOutGetCaps.Call(uintptr(i), uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if r == 0 {
			name := syscall.UTF16ToString(caps.szPname[:])
			log.Printf("[AudioService] WaveOut #%d: %s", i, name)
			upper := strings.ToUpper(name)
			if (strings.Contains(upper, "CABLE") || strings.Contains(upper, "VB-AUDIO")) && targetID == 0xFFFFFFFF {
				targetID = i
			}
		}
	}
	if targetID != 0xFFFFFFFF {
		log.Printf("[AudioService] CABLE Input device selected: ID=%d", targetID)
		return targetID
	}
	log.Printf("[AudioService] VB-Cable not found by name, falling back to device 0")
	return 0
}

func NewWavePlayer() *WavePlayer {
	wfx := WAVEFORMATEX{
		wFormatTag:      1, // PCM
		nChannels:       1, // Mono
		nSamplesPerSec:  16000,
		nAvgBytesPerSec: 32000,
		nBlockAlign:     2,
		wBitsPerSample:  16,
		cbSize:          0,
	}

	devID := findCableInputDeviceID()
	var hWaveOut syscall.Handle
	r, _, _ := procWaveOutOpen.Call(
		uintptr(unsafe.Pointer(&hWaveOut)),
		uintptr(devID),
		uintptr(unsafe.Pointer(&wfx)),
		0, 0, 0,
	)

	if r != 0 {
		log.Printf("[AudioService] waveOut open error (code: %d)", r)
		return &WavePlayer{opened: false}
	}

	log.Printf("[AudioService] CABLE Input opened successfully (16kHz 16-bit PCM Mono)!")
	return &WavePlayer{hWaveOut: hWaveOut, opened: true}
}

func (p *WavePlayer) Write(pcmData []byte) {
	if p == nil || !p.opened || len(pcmData) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	bufCopy := make([]byte, len(pcmData))
	copy(bufCopy, pcmData)

	hdr := &WAVEHDR{
		lpData:         uintptr(unsafe.Pointer(&bufCopy[0])),
		dwBufferLength: uint32(len(bufCopy)),
	}

	r, _, _ := procWaveOutPrepare.Call(uintptr(p.hWaveOut), uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
	if r != 0 {
		return
	}

	procWaveOutWrite.Call(uintptr(p.hWaveOut), uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))

	go func(h syscall.Handle, header *WAVEHDR, buf []byte) {
		time.Sleep(300 * time.Millisecond)
		procWaveOutUnprep.Call(uintptr(h), uintptr(unsafe.Pointer(header)), unsafe.Sizeof(*header))
		_ = buf
	}(p.hWaveOut, hdr, bufCopy)
}

func (p *WavePlayer) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.opened && p.hWaveOut != 0 {
		procWaveOutClose.Call(uintptr(p.hWaveOut))
		p.hWaveOut = 0
		p.opened = false
	}
}

func resolveRoomName(room string) string {
	clean := strings.ToLower(strings.TrimSpace(room))
	if clean == "" || clean == "auto" || clean == "%computername%" {
		if comp := os.Getenv("COMPUTERNAME"); comp != "" {
			return strings.ToLower(comp)
		}
		if host, err := os.Hostname(); err == nil && host != "" {
			return strings.ToLower(host)
		}
		return "default"
	}
	return clean
}

func main() {
	exePath, _ := os.Executable()
	logDir := filepath.Dir(exePath)
	logFile, err := os.OpenFile(filepath.Join(logDir, "webcam_audio.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	if err == nil {
		log.SetOutput(logFile)
	}
	log.Printf("=== VirtualCam Audio Service Avviato (PID: %d) ===", os.Getpid())

	relayURL := "wss://guacamole.reload.local/cam/ws"
	roomName := "auto"
	cfgFile := filepath.Join(logDir, "config.json")
	if data, err := os.ReadFile(cfgFile); err == nil {
		var cfg struct {
			RelayURL string `json:"relay_url"`
			Room     string `json:"room"`
		}
		if err := json.Unmarshal(data, &cfg); err == nil {
			if cfg.RelayURL != "" {
				relayURL = cfg.RelayURL
			}
			if cfg.Room != "" {
				roomName = cfg.Room
			}
		}
	}
	if envURL := os.Getenv("VIRTUALCAM_RELAY_URL"); envURL != "" {
		relayURL = envURL
	}
	if envRoom := os.Getenv("VIRTUALCAM_ROOM"); envRoom != "" {
		roomName = envRoom
	}
	roomName = resolveRoomName(roomName)
	log.Printf("[AudioService] Configuration: RelayURL=%s, Room=%s", relayURL, roomName)

	u, err := url.Parse(relayURL)
	if err != nil {
		log.Fatalf("Invalid relay URL: %v", err)
	}

	q := u.Query()
	q.Set("role", "receiver")
	q.Set("room", roomName)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
		HandshakeTimeout: 8 * time.Second,
	}

	player := NewWavePlayer()
	defer player.Close()

	for {
		log.Printf("[AudioService] Connecting to %s...", u.String())
		conn, _, err := dialer.Dial(u.String(), nil)
		if err != nil {
			log.Printf("[AudioService] Connection failed (%v). Retrying in 3s...", err)
			time.Sleep(3 * time.Second)
			continue
		}

		log.Printf("[AudioService] Connected successfully to WebSocket relay!")

		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[AudioService] Connection lost (%v). Reconnecting...", err)
				conn.Close()
				break
			}

			if msgType == websocket.BinaryMessage {
				if len(data) >= 12 && data[0] == 'A' && data[1] == 'C' && data[2] == 'A' && data[3] == 'M' {
					player.Write(data[12:])
				}
			}
		}

		time.Sleep(2 * time.Second)
	}
}

