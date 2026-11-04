package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	"github.com/gorilla/websocket"
	"golang.org/x/sys/windows/registry"
)

//go:embed driver_embed/*
var embeddedDriver embed.FS

//go:embed icon.ico
var iconData []byte

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	shell32                 = syscall.NewLazyDLL("shell32.dll")
	user32                  = syscall.NewLazyDLL("user32.dll")
	winmm                   = syscall.NewLazyDLL("winmm.dll")
	procCreateFileMappingW  = kernel32.NewProc("CreateFileMappingW")
	procMapViewOfFile       = kernel32.NewProc("MapViewOfFile")
	procUnmapViewOfFile     = kernel32.NewProc("UnmapViewOfFile")
	procCreateMutexW        = kernel32.NewProc("CreateMutexW")
	procReleaseMutex        = kernel32.NewProc("ReleaseMutex")
	procCreateEventW        = kernel32.NewProc("CreateEventW")
	procSetEvent            = kernel32.NewProc("SetEvent")
	procWaitForSingleObject = kernel32.NewProc("WaitForSingleObject")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
	procShellExecuteW       = shell32.NewProc("ShellExecuteW")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procWaveOutOpen         = winmm.NewProc("waveOutOpen")
	procWaveOutWrite        = winmm.NewProc("waveOutWrite")
	procWaveOutPrepare      = winmm.NewProc("waveOutPrepareHeader")
	procWaveOutUnprep       = winmm.NewProc("waveOutUnprepareHeader")
	procWaveOutClose        = winmm.NewProc("waveOutClose")
	procWaveOutGetNum       = winmm.NewProc("waveOutGetNumDevs")
	procWaveOutGetCapsW     = winmm.NewProc("waveOutGetDevCapsW")
)

const (
	PAGE_READWRITE      = 0x04
	FILE_MAP_WRITE      = 0x0002
	WAIT_OBJECT_0       = 0x00000000
	WAIT_ABANDONED      = 0x00000080
	WAIT_TIMEOUT        = 0x00000102
	MAX_SHARED_IMG_SIZE = 3840 * 2160 * 4 * 2 // 66 MB (supporta fino a 4K)
	HEADER_SIZE         = 32
	REG_RUN_KEY         = `Software\Microsoft\Windows\CurrentVersion\Run`
	APP_NAME            = "VirtualWebcamReceiver"
	LOG_FILENAME        = "webcam_receiver.log"
)

// Logger thread-safe su file con auto-flush e rotazione automatica
type Logger struct {
	file    *os.File
	path    string
	mu      sync.Mutex
}

var globalLogger *Logger

func initLogger() *Logger {
	exePath, err := os.Executable()
	var logDir string
	if err == nil {
		logDir = filepath.Dir(exePath)
	} else {
		logDir = "."
	}

	logPath := filepath.Join(logDir, LOG_FILENAME)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	if err != nil {
		// Fallback su %LOCALAPPDATA% se la cartella dell'eseguibile è di sola lettura
		fallbackDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "VirtualWebcam")
		_ = os.MkdirAll(fallbackDir, 0755)
		logPath = filepath.Join(fallbackDir, LOG_FILENAME)
		f, _ = os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	}

	// Rotazione log se supera i 5 MB
	if f != nil {
		if fi, err := f.Stat(); err == nil && fi.Size() > 5*1024*1024 {
			_ = f.Close()
			_ = os.Rename(logPath, logPath+".old")
			f, _ = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
		}
	}

	l := &Logger{file: f, path: logPath}
	l.Log("INFO", "==========================================================")
	l.Log("INFO", "VirtualCam Receiver started - PID: %d", os.Getpid())
	l.Log("INFO", "Active log file at: %s", logPath)
	l.Log("INFO", "Executable path: %s", exePath)
	l.Log("INFO", "==========================================================")
	return l
}

func (l *Logger) Log(level string, format string, args ...interface{}) {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] [%s] %s\r\n", ts, level, msg)
	_, _ = l.file.WriteString(line)
	_ = l.file.Sync()
}

func (l *Logger) RecoverPanic(context string) {
	if r := recover(); r != nil {
		stack := debug.Stack()
		l.Log("FATAL", "CRASH / PANIC catturato in [%s]: %v\r\nStack trace:\r\n%s", context, r, string(stack))
	}
}

type Config struct {
	RelayURL string `json:"relay_url"`
	Room     string `json:"room"`
	Token    string `json:"token"`
}

type VirtualCamSharedMemory struct {
	hMutex              syscall.Handle
	hWantEvent          syscall.Handle
	hSentEvent          syscall.Handle
	hFileMap            syscall.Handle
	pSharedBuf          uintptr
	bufSize             uint32
	consecutiveFailures int
	mu                  sync.Mutex
}

func newVirtualCamSharedMemory() (*VirtualCamSharedMemory, error) {
	v := &VirtualCamSharedMemory{
		bufSize: uint32(HEADER_SIZE + MAX_SHARED_IMG_SIZE),
	}

	mutexName, _ := syscall.UTF16PtrFromString("UnityCapture_Mutx")
	wantEventName, _ := syscall.UTF16PtrFromString("UnityCapture_Want")
	sentEventName, _ := syscall.UTF16PtrFromString("UnityCapture_Sent")
	dataName, _ := syscall.UTF16PtrFromString("UnityCapture_Data")

	hMutex, _, _ := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(mutexName)))
	if hMutex == 0 {
		return nil, fmt.Errorf("impossibile creare Mutex Win32 (UnityCapture_Mutx)")
	}
	v.hMutex = syscall.Handle(hMutex)

	hWant, _, _ := procCreateEventW.Call(0, 0, 0, uintptr(unsafe.Pointer(wantEventName)))
	if hWant == 0 {
		procCloseHandle.Call(uintptr(v.hMutex))
		return nil, fmt.Errorf("impossibile creare Event Want")
	}
	v.hWantEvent = syscall.Handle(hWant)

	hSent, _, _ := procCreateEventW.Call(0, 0, 0, uintptr(unsafe.Pointer(sentEventName)))
	if hSent == 0 {
		procCloseHandle.Call(uintptr(v.hMutex))
		procCloseHandle.Call(uintptr(v.hWantEvent))
		return nil, fmt.Errorf("impossibile creare Event Sent")
	}
	v.hSentEvent = syscall.Handle(hSent)

	hFile, _, _ := procCreateFileMappingW.Call(
		uintptr(syscall.InvalidHandle),
		0,
		PAGE_READWRITE,
		0,
		uintptr(v.bufSize),
		uintptr(unsafe.Pointer(dataName)),
	)
	if hFile == 0 {
		procCloseHandle.Call(uintptr(v.hMutex))
		procCloseHandle.Call(uintptr(v.hWantEvent))
		procCloseHandle.Call(uintptr(v.hSentEvent))
		return nil, fmt.Errorf("impossibile creare FileMapping Win32 (UnityCapture_Data)")
	}
	v.hFileMap = syscall.Handle(hFile)

	pBuf, _, _ := procMapViewOfFile.Call(hFile, FILE_MAP_WRITE, 0, 0, 0)
	if pBuf == 0 {
		procCloseHandle.Call(uintptr(v.hFileMap))
		procCloseHandle.Call(uintptr(v.hMutex))
		procCloseHandle.Call(uintptr(v.hWantEvent))
		procCloseHandle.Call(uintptr(v.hSentEvent))
		return nil, fmt.Errorf("impossibile mappare View Of File Win32")
	}
	v.pSharedBuf = pBuf
	base := v.pSharedBuf
	*(*uint32)(unsafe.Pointer(base + 0)) = MAX_SHARED_IMG_SIZE
	*(*int32)(unsafe.Pointer(base + 4)) = 1280
	*(*int32)(unsafe.Pointer(base + 8)) = 720
	*(*int32)(unsafe.Pointer(base + 12)) = 1280
	*(*int32)(unsafe.Pointer(base + 16)) = 0 // FORMAT_UINT8
	*(*int32)(unsafe.Pointer(base + 20)) = 1 // RESIZEMODE_LINEAR
	*(*int32)(unsafe.Pointer(base + 24)) = 0 // MIRRORMODE_DISABLED
	*(*int32)(unsafe.Pointer(base + 28)) = 5000 // 5000ms timeout

	// Inizializza con pixel scuri (#0f172a): mai più schermo verde all'avvio prima della ricezione
	initBytes := 1280 * 720 * 4
	if uint32(initBytes) <= MAX_SHARED_IMG_SIZE {
		initialPixels := unsafe.Slice((*byte)(unsafe.Pointer(base+HEADER_SIZE)), initBytes)
		for i := 0; i < len(initialPixels); i += 4 {
			initialPixels[i+0] = 15  // R
			initialPixels[i+1] = 23  // G
			initialPixels[i+2] = 42  // B
			initialPixels[i+3] = 255 // A
		}
		procSetEvent.Call(uintptr(v.hSentEvent))
	}

	globalLogger.Log("INFO", "Shared memory DirectShow inizializzata con successo (Buffer: %d MB)", v.bufSize/(1024*1024))
	return v, nil
}

func (v *VirtualCamSharedMemory) resetMutex() {
	if v.hMutex != 0 {
		procCloseHandle.Call(uintptr(v.hMutex))
		v.hMutex = 0
	}
	mutexName, _ := syscall.UTF16PtrFromString("UnityCapture_Mutx")
	hMutex, _, _ := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(mutexName)))
	if hMutex != 0 {
		v.hMutex = syscall.Handle(hMutex)
		globalLogger.Log("INFO", "Handle Mutex ricreato con successo (0x%x)", v.hMutex)
	}
}

func (v *VirtualCamSharedMemory) Close() {
	if v.pSharedBuf != 0 {
		procUnmapViewOfFile.Call(v.pSharedBuf)
		v.pSharedBuf = 0
	}
	if v.hFileMap != 0 {
		procCloseHandle.Call(uintptr(v.hFileMap))
		v.hFileMap = 0
	}
	if v.hMutex != 0 {
		procCloseHandle.Call(uintptr(v.hMutex))
		v.hMutex = 0
	}
	if v.hWantEvent != 0 {
		procCloseHandle.Call(uintptr(v.hWantEvent))
		v.hWantEvent = 0
	}
	if v.hSentEvent != 0 {
		procCloseHandle.Call(uintptr(v.hSentEvent))
		v.hSentEvent = 0
	}
}

func (v *VirtualCamSharedMemory) SendFrame(width, height int, rgbaPixels []byte) error {
	if v == nil || v.pSharedBuf == 0 {
		return fmt.Errorf("shared memory non inizializzata")
	}
	if width <= 0 || height <= 0 || len(rgbaPixels) == 0 {
		return fmt.Errorf("parametri frame non validi (%dx%d, len: %d)", width, height, len(rgbaPixels))
	}

	dataSize := uint32(len(rgbaPixels))
	if dataSize > MAX_SHARED_IMG_SIZE {
		return fmt.Errorf("dimensione frame %d supera il buffer massimo %d", dataSize, MAX_SHARED_IMG_SIZE)
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	// Timeout a 100ms. Nota: WAIT_ABANDONED (0x80) significa che un processo precedente è terminato
	// e il kernel Windows ha correttamente trasferito a noi la proprietà del mutex!
	ret, _, _ := procWaitForSingleObject.Call(uintptr(v.hMutex), 100)
	if ret != WAIT_OBJECT_0 && ret != WAIT_ABANDONED {
		v.consecutiveFailures++
		if v.consecutiveFailures >= 5 {
			v.consecutiveFailures = 0
			globalLogger.Log("WARN", "Rilevati 5 timeout consecutivi sul mutex (ret=0x%x). Ripristino handle...", ret)
			v.resetMutex()
		}
		return fmt.Errorf("timeout acquisizione mutex (ret=0x%x)", ret)
	}
	v.consecutiveFailures = 0

	// IMPORTANTE: Rilascio del mutex con verifica esito
	defer func() {
		ok, _, _ := procReleaseMutex.Call(uintptr(v.hMutex))
		if ok == 0 {
			errCode, _, _ := kernel32.NewProc("GetLastError").Call()
			globalLogger.Log("WARN", "ReleaseMutex fallito con codice errore: %d", errCode)
		}
	}()

	base := v.pSharedBuf
	stride := int32(width) // In pixel (uint32)

	*(*uint32)(unsafe.Pointer(base + 0)) = MAX_SHARED_IMG_SIZE
	*(*int32)(unsafe.Pointer(base + 4)) = int32(width)
	*(*int32)(unsafe.Pointer(base + 8)) = int32(height)
	*(*int32)(unsafe.Pointer(base + 12)) = stride
	*(*int32)(unsafe.Pointer(base + 16)) = 0 // FORMAT_UINT8
	*(*int32)(unsafe.Pointer(base + 20)) = 1 // RESIZEMODE_LINEAR
	*(*int32)(unsafe.Pointer(base + 24)) = 0 // MIRRORMODE_DISABLED
	*(*int32)(unsafe.Pointer(base + 28)) = 5000 // 5000ms timeout per prevenire freeze del driver DirectShow

	dstSlice := unsafe.Slice((*byte)(unsafe.Pointer(base+HEADER_SIZE)), dataSize)

	// Flip verticale: le immagini JPEG partono dall'alto verso il basso,
	// mentre DirectShow DIB si aspetta riga 0 in basso. Raddrizza il video al 100%.
	rowSize := width * 4
	for y := 0; y < height; y++ {
		srcOff := y * rowSize
		dstOff := (height - 1 - y) * rowSize
		if srcOff+rowSize <= len(rgbaPixels) && dstOff+rowSize <= len(dstSlice) {
			copy(dstSlice[dstOff:dstOff+rowSize], rgbaPixels[srcOff:srcOff+rowSize])
		}
	}

	procSetEvent.Call(uintptr(v.hSentEvent))
	return nil
}

func isDriverInstalled() bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, `CLSID\{860BB310-5D01-11d0-BD3B-00A0C911CE86}\Instance`, registry.ENUMERATE_SUB_KEYS|registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()

	subkeys, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return false
	}
	for _, sk := range subkeys {
		if strings.EqualFold(sk, "{5C2CD55C-92AD-4999-8666-912BD3E70010}") {
			return true
		}
		subKey, err := registry.OpenKey(k, sk, registry.READ)
		if err != nil {
			continue
		}
		name, _, err := subKey.GetStringValue("FriendlyName")
		subKey.Close()
		if err == nil {
			upper := strings.ToUpper(name)
			if strings.Contains(upper, "UNITY") || strings.Contains(upper, "VIRTUAL") || strings.Contains(upper, "CAPTURE") {
				return true
			}
		}
	}
	return false
}

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

type WaveAudioPlayer struct {
	hWaveOut syscall.Handle
	opened   bool
	mu       sync.Mutex
}

func findCableInputDeviceID() uint32 {
	numDevs, _, _ := procWaveOutGetNum.Call()
	targetID := uint32(0xFFFFFFFF)
	var foundName string
	for i := uint32(0); i < uint32(numDevs); i++ {
		var caps WAVEOUTCAPSW
		r, _, _ := procWaveOutGetCapsW.Call(uintptr(i), uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if r == 0 {
			name := syscall.UTF16ToString(caps.szPname[:])
			globalLogger.Log("INFO", "Dispositivo waveOut #%d: %s", i, name)
			upper := strings.ToUpper(name)
			if (strings.Contains(upper, "CABLE") || strings.Contains(upper, "VB-AUDIO")) && targetID == 0xFFFFFFFF {
				targetID = i
				foundName = name
			}
		}
	}
	if targetID != 0xFFFFFFFF {
		globalLogger.Log("INFO", "Dispositivo audio virtuale rilevato e selezionato: ID=%d (%s)", targetID, foundName)
		return targetID
	}
	globalLogger.Log("WARN", "Nessun dispositivo VB-CABLE specifico trovato, uso WAVE_MAPPER predefinito (0xFFFFFFFF)")
	return 0xFFFFFFFF // WAVE_MAPPER
}

func NewWaveAudioPlayer() *WaveAudioPlayer {
	wfx := WAVEFORMATEX{
		wFormatTag:      1, // WAVE_FORMAT_PCM
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
		globalLogger.Log("WARN", "Impossibile aprire waveOut per microfono (err=%d)", r)
		return &WaveAudioPlayer{opened: false}
	}

	globalLogger.Log("INFO", "Dispositivo audio aperto con successo per Virtual Webcam Mic (16kHz PCM)")
	return &WaveAudioPlayer{hWaveOut: hWaveOut, opened: true}
}

func (p *WaveAudioPlayer) Write(pcmData []byte) {
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
		time.Sleep(150 * time.Millisecond)
		procWaveOutUnprep.Call(uintptr(h), uintptr(unsafe.Pointer(header)), unsafe.Sizeof(*header))
		_ = buf
	}(p.hWaveOut, hdr, bufCopy)
}

func (p *WaveAudioPlayer) Close() {
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

func ensureDriverInstalled() error {
	if isDriverInstalled() {
		globalLogger.Log("INFO", "DirectShow Virtual Webcam driver already registered in system.")
		return nil
	}

	globalLogger.Log("INFO", "DirectShow driver not found. Starting automatic extraction and registration...")
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = os.Getenv("APPDATA")
	}
	driverDir := filepath.Join(localAppData, "VirtualWebcam", "driver")
	if err := os.MkdirAll(driverDir, 0755); err != nil {
		return fmt.Errorf("impossibile creare cartella driver: %w", err)
	}

	dll64Path := filepath.Join(driverDir, "UnityCaptureFilter64.dll")
	dll32Path := filepath.Join(driverDir, "UnityCaptureFilter32.dll")

	dll64Data, err := embeddedDriver.ReadFile("driver_embed/UnityCaptureFilter64.dll")
	if err == nil {
		_ = os.WriteFile(dll64Path, dll64Data, 0644)
	}
	dll32Data, err := embeddedDriver.ReadFile("driver_embed/UnityCaptureFilter32.dll")
	if err == nil {
		_ = os.WriteFile(dll32Path, dll32Data, 0644)
	}

	is64Bit := unsafe.Sizeof(uintptr(0)) == 8
	regTarget := dll64Path
	if !is64Bit {
		regTarget = dll32Path
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "regsvr32.exe", "/s", regTarget)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		globalLogger.Log("ERROR", "Registrazione con regsvr32 completata con errore o timeout: %v", err)
		return err
	}

	globalLogger.Log("INFO", "Driver DirectShow (%s) registrato con successo!", regTarget)
	return nil
}

func isAutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, REG_RUN_KEY, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(APP_NAME)
	return err == nil
}

func toggleAutostart(enable bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, REG_RUN_KEY, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()

	if enable {
		exePath, err := os.Executable()
		if err == nil {
			_ = k.SetStringValue(APP_NAME, fmt.Sprintf("\"%s\"", exePath))
			globalLogger.Log("INFO", "Avvio automatico con Windows abilitato: %s", exePath)
		}
	} else {
		_ = k.DeleteValue(APP_NAME)
		globalLogger.Log("INFO", "Avvio automatico con Windows disabilitato")
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

func getConfigPath() string {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = os.Getenv("APPDATA")
	}
	dir := filepath.Join(localAppData, "VirtualWebcam")
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "config.json")
}

func loadOrCreateConfig() Config {
	cfg := Config{
		RelayURL: "wss://guacamole.reload.local/cam/ws",
		Room:     "auto",
		Token:    "",
	}

	// 1. Check config.json in executable directory (deployed via GPO)
	foundGlobal := false
	if exePath, err := os.Executable(); err == nil {
		globalCfgPath := filepath.Join(filepath.Dir(exePath), "config.json")
		if data, err := os.ReadFile(globalCfgPath); err == nil {
			if err := json.Unmarshal(data, &cfg); err == nil && cfg.RelayURL != "" {
				foundGlobal = true
			}
		}
	}

	// 2. Fallback to user configuration in %LOCALAPPDATA%
	if !foundGlobal {
		cfgPath := getConfigPath()
		data, err := os.ReadFile(cfgPath)
		if err == nil {
			json.Unmarshal(data, &cfg)
		} else {
			d, _ := json.MarshalIndent(cfg, "", "  ")
			_ = os.WriteFile(cfgPath, d, 0644)
		}
	}

	// 3. Environment variable overrides (if set)
	if envURL := os.Getenv("VIRTUALCAM_RELAY_URL"); envURL != "" {
		cfg.RelayURL = envURL
	}
	if envRoom := os.Getenv("VIRTUALCAM_ROOM"); envRoom != "" {
		cfg.Room = envRoom
	}
	if envToken := os.Getenv("VIRTUALCAM_TOKEN"); envToken != "" {
		cfg.Token = envToken
	}

	cfg.Room = resolveRoomName(cfg.Room)
	return cfg
}

var (
	mStatus     *systray.MenuItem
	mStats      *systray.MenuItem
	mDeviceItem *systray.MenuItem
	mMicItem    *systray.MenuItem
	mSourceItem *systray.MenuItem
	mAutostart  *systray.MenuItem
	globalCfg   Config
	stopChan    = make(chan struct{})
)

func main() {
	globalLogger = initLogger()
	defer globalLogger.RecoverPanic("main")

	globalCfg = loadOrCreateConfig()
	globalLogger.Log("INFO", "Configurazione caricata: RelayURL=%s, Room=%s", globalCfg.RelayURL, globalCfg.Room)

	_ = ensureDriverInstalled()
	systray.Run(onReady, onExit)
}

func onReady() {
	defer globalLogger.RecoverPanic("onReady")

	systray.SetIcon(iconData)
	systray.SetTitle("Virtual Webcam")
	systray.SetTooltip(fmt.Sprintf("Virtual Webcam [%s] - Connecting...", strings.ToUpper(globalCfg.Room)))

	mHeader := systray.AddMenuItem(fmt.Sprintf("📥 Virtual Webcam Receiver [%s]", strings.ToUpper(globalCfg.Room)), "Status")
	mHeader.Disable()

	mStatus = systray.AddMenuItem("🟡 Connecting to Relay...", "Server status")
	mStatus.Disable()

	mStats = systray.AddMenuItem("Waiting for video frames...", "Streaming details")
	mStats.Disable()

	systray.AddSeparator()

	mRecvTitle := systray.AddMenuItem("🔌 Virtualized Devices:", "Device list")
	mRecvTitle.Disable()

	mDeviceItem = systray.AddMenuItem("  • 🟢 Virtual Webcam [DirectShow Virtual Video]", "Virtual system video device")
	mDeviceItem.Disable()

	mMicItem = systray.AddMenuItem("  • 🟢 Virtual Webcam Mic [Virtual Audio Microphone]", "Virtual audio microphone device")
	mMicItem.Disable()

	mSourceItem = systray.AddMenuItem("    └ Source: Remote Webcam", "Stream origin")
	mSourceItem.Disable()

	mTargetInfo := systray.AddMenuItem("    └ Supported in: Google Meet, Teams, 3CX, Zoom", "Compatibility")
	mTargetInfo.Disable()

	systray.AddSeparator()

	isAuto := isAutostartEnabled()
	mAutostart = systray.AddMenuItemCheckbox("Start automatically with Windows", "Run on user logon", isAuto)

	mOpenWeb := systray.AddMenuItem("Open Webcam Sender Web Page (/cam)", "Open browser")
	mOpenLog := systray.AddMenuItem("📄 Open Debug Log", "View diagnostic log in Notepad")
	mOpenConfig := systray.AddMenuItem("Open Configuration Folder", "Edit relay address")

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Exit Virtual Webcam", "Close application")

	go runReceiverEngine()

	go func() {
		defer globalLogger.RecoverPanic("systrayEvents")
		for {
			select {
			case <-mAutostart.ClickedCh:
				if mAutostart.Checked() {
					toggleAutostart(false)
					mAutostart.Uncheck()
				} else {
					toggleAutostart(true)
					mAutostart.Check()
				}
			case <-mOpenWeb.ClickedCh:
				webURL := "https://192.168.0.13/cam/"
				_ = exec.Command("cmd", "/c", "start", webURL).Start()
			case <-mOpenLog.ClickedCh:
				if globalLogger != nil && globalLogger.path != "" {
					_ = exec.Command("notepad.exe", globalLogger.path).Start()
				}
			case <-mOpenConfig.ClickedCh:
				dir := filepath.Dir(getConfigPath())
				_ = exec.Command("explorer", dir).Start()
			case <-mQuit.ClickedCh:
				globalLogger.Log("INFO", "Chiusura applicazione richiesta dall'utente.")
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	close(stopChan)
}

func runReceiverEngine() {
	defer globalLogger.RecoverPanic("runReceiverEngine")

	vcam, err := newVirtualCamSharedMemory()
	if err != nil {
		globalLogger.Log("FATAL", "Inizializzazione driver fallita: %v", err)
		mStatus.SetTitle("❌ Errore inizializzazione driver")
		return
	}
	defer vcam.Close()

	audioPlayer := NewWaveAudioPlayer()
	defer audioPlayer.Close()

	u, err := url.Parse(globalCfg.RelayURL)
	if err != nil {
		globalLogger.Log("ERROR", "URL relay non valido (%s): %v", globalCfg.RelayURL, err)
		mStatus.SetTitle("❌ URL non valido")
		return
	}

	q := u.Query()
	q.Set("role", "receiver")
	q.Set("room", globalCfg.Room)
	if globalCfg.Token != "" {
		q.Set("token", globalCfg.Token)
	}
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		HandshakeTimeout: 8 * time.Second,
	}

	var (
		totalFrames    uint64
		fpsCount       uint64
		bytesCount     uint64
		audioPackets   uint64
		currentRes     = "720p"
		silentSecs     = 0
		isSenderActive = false
	)

	type frameItem struct {
		width   int
		height  int
		pixels  []byte
		rawBuf  []byte
		dataLen int
	}

	// Buffer pool riciclabile: elimina completamente le allocazioni continue (3.7 MB/frame)
	// e azzera le pause del Garbage Collector che causavano microscatti
	bufferPool := make(chan []byte, 10)
	for i := 0; i < 6; i++ {
		bufferPool <- make([]byte, 1920*1080*4)
	}

	// Coda da 8 frame per assorbire i micro-burst TCP della rete senza perdere frame
	frameChan := make(chan *frameItem, 8)

	// WORKER DEDICATO CON THREAD OS VINCOLATO:
	// runtime.LockOSThread() vincola PERMANENTEMENTE questa goroutine a un singolo thread OS Win32.
	// Questo è INDISPENSABILE per i Mutex Win32: se la goroutine migrasse tra thread OS durante l'attesa,
	// ReleaseMutex() fallirebbe con ERROR_NOT_OWNER (288), bloccando il mutex e congelando il flusso!
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer globalLogger.RecoverPanic("frameConsumer")

		var lastFrame *frameItem
		keepAliveTicker := time.NewTicker(150 * time.Millisecond)
		defer keepAliveTicker.Stop()

		for {
			select {
			case <-stopChan:
				return
			case frame, ok := <-frameChan:
				if !ok {
					return
				}
				lastFrame = frame
				if err := vcam.SendFrame(frame.width, frame.height, frame.pixels); err == nil {
					fpsCount++
					totalFrames++
					bytesCount += uint64(frame.dataLen)
					currentRes = fmt.Sprintf("%dx%d", frame.width, frame.height)
				} else {
					globalLogger.Log("WARN", "SendFrame fallito: %v", err)
				}

				if frame.rawBuf != nil {
					select {
					case bufferPool <- frame.rawBuf:
					default:
					}
				}
			case <-keepAliveTicker.C:
				// Se non arrivano nuovi frame dal network per oltre 150ms ma il flusso è attivo,
				// ripete l'ultimo frame valido nel driver DirectShow per impedire il timeout e lo schermo verde.
				if lastFrame != nil && isSenderActive {
					_ = vcam.SendFrame(lastFrame.width, lastFrame.height, lastFrame.pixels)
				}
			}
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	go func() {
		defer globalLogger.RecoverPanic("statsTicker")
		for {
			select {
			case <-ticker.C:
				if fpsCount > 0 || audioPackets > 0 {
					kbps := (bytesCount * 8) / 1024
					mStats.SetTitle(fmt.Sprintf("📊 %s | %d FPS | %d Kbps | Frame: %d", currentRes, fpsCount, kbps, totalFrames))
					mDeviceItem.SetTitle(fmt.Sprintf("  • 🟢 Virtual Webcam [%s @ %d FPS]", currentRes, fpsCount))
					if audioPackets > 0 {
						mMicItem.SetTitle(fmt.Sprintf("  • 🟢 Virtual Webcam Mic [Active: %d pkt/s]", audioPackets))
					} else {
						mMicItem.SetTitle("  • 🟢 Virtual Webcam Mic [Ready]")
					}
					mStatus.SetTitle("🟢 Streaming from Remote Webcam")
					systray.SetTooltip(fmt.Sprintf("Virtual Webcam: 🟢 Active (%s @ %d FPS)", currentRes, fpsCount))

					if !isSenderActive {
						isSenderActive = true
						globalLogger.Log("INFO", "Active stream from Remote Webcam: %s @ %d FPS (%d Kbps, Audio: %d pkt/s)", currentRes, fpsCount, kbps, audioPackets)
					}
					silentSecs = 0
				} else {
					silentSecs++
					if silentSecs >= 3 && isSenderActive {
						isSenderActive = false
						mStats.SetTitle("Waiting for data from Remote Webcam...")
						mDeviceItem.SetTitle("  • 🟡 Virtual Webcam [Waiting]")
						mMicItem.SetTitle("  • 🟡 Virtual Webcam Mic [Waiting]")
						mStatus.SetTitle("🟡 Remote Webcam waiting")
						systray.SetTooltip("Virtual Webcam: 🟡 Waiting for Remote Webcam")
						globalLogger.Log("INFO", "No data received in last 3s. Status: Waiting")
					}
				}
				fpsCount = 0
				audioPackets = 0
				bytesCount = 0
			case <-stopChan:
				return
			}
		}
	}()

	for {
		select {
		case <-stopChan:
			return
		default:
		}

		mStatus.SetTitle("🟡 Connecting to Relay...")
		systray.SetTooltip("Virtual Webcam - Connecting to Relay...")
		globalLogger.Log("INFO", "Connecting to WebSocket relay: %s", u.String())

		conn, _, err := dialer.Dial(u.String(), nil)
		if err != nil {
			globalLogger.Log("WARN", "Relay unreachable (%v). Retrying in 3 seconds...", err)
			mStatus.SetTitle("🔴 Relay unreachable (retrying in 3s)")
			systray.SetTooltip("Virtual Webcam: 🔴 Disconnected")
			time.Sleep(3 * time.Second)
			continue
		}

		globalLogger.Log("INFO", "Connected successfully to WebSocket relay!")
		mStatus.SetTitle("🟢 Connected to Relay - Ready")
		systray.SetTooltip("Virtual Webcam: 🟢 Connected (Ready)")

		reqMsg, _ := json.Marshal(map[string]string{"type": "request_keyframe"})
		_ = conn.WriteMessage(websocket.TextMessage, reqMsg)

		for {
			select {
			case <-stopChan:
				conn.Close()
				return
			default:
			}

			msgType, data, err := conn.ReadMessage()
			if err != nil {
				globalLogger.Log("WARN", "Connection closed (%v). Auto-reconnecting...", err)
				conn.Close()
				break
			}

			if msgType == websocket.TextMessage {
				var msg map[string]interface{}
				if err := json.Unmarshal(data, &msg); err == nil {
					if evt, ok := msg["event"].(string); ok && evt == "sender_online" {
						if online, ok := msg["status"].(bool); ok {
							if online {
								mStatus.SetTitle("🟢 Remote Webcam Online (Webcam active)")
								mSourceItem.SetTitle(fmt.Sprintf("    └ Source: Remote Webcam (Room %s - ON)", globalCfg.Room))
								_ = conn.WriteMessage(websocket.TextMessage, reqMsg)
								systray.SetTooltip(fmt.Sprintf("Virtual Webcam [%s]: 🟢 Streaming active", strings.ToUpper(globalCfg.Room)))
								globalLogger.Log("INFO", "Event notification: Remote Webcam Online!")
							} else {
								mStatus.SetTitle("🟡 Remote Webcam waiting (Webcam idle)")
								mSourceItem.SetTitle(fmt.Sprintf("    └ Source: Remote Webcam (Waiting on room %s)", globalCfg.Room))
								mStats.SetTitle("Start webcam from sender to begin")
								systray.SetTooltip(fmt.Sprintf("Virtual Webcam [%s]: 🟡 Waiting for Remote Webcam", strings.ToUpper(globalCfg.Room)))
								globalLogger.Log("INFO", "Event notification: Remote Webcam Offline")
							}
						}
					}
				}
				continue
			}

			if msgType == websocket.BinaryMessage {
				if len(data) < 12 {
					continue
				}

				// Rileva pacchetti Audio ("ACAM") per microfono virtuale
				if data[0] == 'A' && data[1] == 'C' && data[2] == 'A' && data[3] == 'M' {
					if audioPlayer != nil {
						audioPlayer.Write(data[12:])
					}
					audioPackets++
					bytesCount += uint64(len(data))
					continue
				}

				// Rileva pacchetti Video ("VCAM")
				if data[0] != 'V' || data[1] != 'C' || data[2] != 'A' || data[3] != 'M' {
					continue
				}

				jpegPayload := data[12:]
				img, err := jpeg.Decode(bytes.NewReader(jpegPayload))
				if err != nil || img == nil {
					continue
				}

				b := img.Bounds()
				realW := b.Dx()
				realH := b.Dy()

				if realW <= 0 || realH <= 0 {
					continue
				}

				neededBytes := realW * realH * 4
				var pixelBuf []byte
				select {
				case pixelBuf = <-bufferPool:
				default:
					pixelBuf = make([]byte, 1920*1080*4)
				}

				if len(pixelBuf) < neededBytes {
					pixelBuf = make([]byte, neededBytes)
				}

				// Conversione accelerata da YCbCr 4:2:0 nativo a RGBA:
				// Azzeramento totale allocazioni heap e zero pause del Garbage Collector
				if yuv, ok := img.(*image.YCbCr); ok && yuv.SubsampleRatio == image.YCbCrSubsampleRatio420 {
					w := realW
					h := realH
					rowSize := w * 4
					for y := 0; y < h; y++ {
						yOff := y * yuv.YStride
						cOff := (y >> 1) * yuv.CStride
						dstOff := y * rowSize
						for x := 0; x < w; x++ {
							yy := yuv.Y[yOff+x]
							cb := yuv.Cb[cOff+(x>>1)]
							cr := yuv.Cr[cOff+(x>>1)]
							r, g, b := color.YCbCrToRGB(yy, cb, cr)
							pixelBuf[dstOff+0] = r
							pixelBuf[dstOff+1] = g
							pixelBuf[dstOff+2] = b
							pixelBuf[dstOff+3] = 255
							dstOff += 4
						}
					}
				} else {
					rgba := image.NewRGBA(b)
					draw.Draw(rgba, b, img, image.Point{}, draw.Src)
					copy(pixelBuf[:neededBytes], rgba.Pix)
				}

				item := &frameItem{
					width:   realW,
					height:  realH,
					pixels:  pixelBuf[:neededBytes],
					rawBuf:  pixelBuf,
					dataLen: len(data),
				}

				select {
				case frameChan <- item:
				default:
					// Se la coda è satura a causa di jitter temporaneo, ricicla il buffer
					select {
					case bufferPool <- pixelBuf:
					default:
					}
				}
			}
		}

		time.Sleep(2 * time.Second)
	}
}
