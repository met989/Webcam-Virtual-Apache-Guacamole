package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows/registry"
)

//go:embed icon.ico
var iconData []byte

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	procEnumWindows         = user32.NewProc("EnumWindows")
	procGetWindowTextW      = user32.NewProc("GetWindowTextW")
	procIsIconic            = user32.NewProc("IsIconic")
	procIsWindow            = user32.NewProc("IsWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procSetWindowLongPtrW   = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW     = user32.NewProc("CallWindowProcW")
)

const (
	REG_RUN_KEY    = `Software\Microsoft\Windows\CurrentVersion\Run`
	APP_NAME       = "WebcamSender"
	SW_HIDE        = 0
	SW_RESTORE     = 9
	LOG_FILENAME   = "webcam_sender.log"
	HWND_TOPMOST   = ^uintptr(0) // -1
	HWND_NOTOPMOST = ^uintptr(1) // -2
	SWP_SHOWWINDOW = 0x0040
	SM_CXSCREEN    = 0
	SM_CYSCREEN    = 1
	GWLP_WNDPROC   = ^uintptr(3) // -4
)

var (
	origWndProc    uintptr
	mOpenGlobal    *systray.MenuItem
	isHiddenGlobal bool
)

// Logger thread-safe on file with auto-flush and log rotation
type Logger struct {
	file *os.File
	path string
	mu   sync.Mutex
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
		fallbackDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "VirtualWebcamSender")
		_ = os.MkdirAll(fallbackDir, 0755)
		logPath = filepath.Join(fallbackDir, LOG_FILENAME)
		f, _ = os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	}

	// Rotate log if exceeds 5 MB
	if f != nil {
		if fi, err := f.Stat(); err == nil && fi.Size() > 5*1024*1024 {
			_ = f.Close()
			_ = os.Rename(logPath, logPath+".old")
			f, _ = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
		}
	}

	l := &Logger{file: f, path: logPath}
	l.Log("INFO", "==========================================================")
	l.Log("INFO", "Webcam Sender started - PID: %d", os.Getpid())
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
		l.Log("FATAL", "CRASH / PANIC caught in [%s]: %v\r\nStack trace:\r\n%s", context, r, string(stack))
	}
}

type Config struct {
	CamURL string `json:"cam_url"`
}

func getConfigPath() string {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = os.Getenv("APPDATA")
	}
	dir := filepath.Join(localAppData, "VirtualWebcamSender")
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "config.json")
}

func loadOrCreateConfig() Config {
	cfg := Config{
		CamURL: "https://guacamole.reload.local/cam/",
	}

	cfgPath := getConfigPath()
	data, err := os.ReadFile(cfgPath)
	if err == nil {
		json.Unmarshal(data, &cfg)
	} else {
		d, _ := json.MarshalIndent(cfg, "", "  ")
		_ = os.WriteFile(cfgPath, d, 0644)
	}
	return cfg
}

func getConnectedVideoDevices() []string {
	var devices []string
	seen := make(map[string]bool)

	// 1. Physical PnP video devices (Atlantis, Logitech, etc.) with 2s timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
		`(Get-CimInstance Win32_PnPEntity | Where-Object { ($_.PNPClass -eq 'Camera' -or $_.PNPClass -eq 'Image') -and $_.Status -eq 'OK' }).Name`)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, l := range lines {
			name := strings.TrimSpace(l)
			if name != "" && !seen[name] {
				seen[name] = true
				devices = append(devices, name+" [Physical USB Webcam]")
			}
		}
	} else {
		globalLogger.Log("WARN", "PnP device scan failed or timed out: %v", err)
	}

	// 2. DirectShow software capture filters (OBS Virtual Camera, etc.)
	k, err := registry.OpenKey(registry.CLASSES_ROOT, `CLSID\{860BB310-5D01-11d0-BD3B-00A0C911CE86}\Instance`, registry.ENUMERATE_SUB_KEYS|registry.READ)
	if err == nil {
		defer k.Close()
		subkeys, _ := k.ReadSubKeyNames(-1)
		for _, sk := range subkeys {
			subKey, err := registry.OpenKey(k, sk, registry.READ)
			if err != nil {
				continue
			}
			name, _, err := subKey.GetStringValue("FriendlyName")
			subKey.Close()
			if err == nil && name != "" && !seen[name] {
				seen[name] = true
				if strings.Contains(strings.ToLower(name), "obs") {
					devices = append(devices, name+" [Virtual]")
				} else {
					devices = append(devices, name)
				}
			}
		}
	}

	return devices
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
			globalLogger.Log("INFO", "Autostart with Windows enabled: %s", exePath)
		}
	} else {
		_ = k.DeleteValue(APP_NAME)
		globalLogger.Log("INFO", "Autostart with Windows disabled")
	}
}

// Find HWND of VirtualCam Sender window
func findCamWindow() syscall.Handle {
	var found syscall.Handle
	cb := syscall.NewCallback(func(hwnd syscall.Handle, lparam uintptr) uintptr {
		var buf [256]uint16
		lenRet, _, _ := procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), 256)
		if lenRet > 0 {
			title := syscall.UTF16ToString(buf[:lenRet])
			if strings.Contains(title, "VirtualCam Sender") {
				found = hwnd
				return 0 // Found, stop
			}
		}
		return 1 // Continue search
	})
	procEnumWindows.Call(cb, 0)
	return found
}

func findBrowserExecutable() string {
	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\Edge\Application\msedge.exe`),
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "msedge.exe"
}

func restoreOrOpenCamWindow(camURL string) {
	hwnd := findCamWindow()
	if hwnd != 0 {
		globalLogger.Log("INFO", "Webcam window found (HWND=%x). Restoring to screen.", hwnd)
		procShowWindow.Call(uintptr(hwnd), SW_RESTORE)
		procSetForegroundWindow.Call(uintptr(hwnd))
		isHiddenGlobal = false
		if mOpenGlobal != nil {
			mOpenGlobal.SetTitle("👁 Minimize to Tray (-)")
		}
		systray.SetTooltip("Webcam Sender - Active")
	} else {
		globalLogger.Log("INFO", "No window open. Starting webcam interface...")
		openCamWindow(camURL)
		isHiddenGlobal = false
		if mOpenGlobal != nil {
			mOpenGlobal.SetTitle("👁 Minimize to Tray (-)")
		}
	}
}

func openCamWindow(camURL string) {
	hwnd := findCamWindow()
	if hwnd != 0 {
		globalLogger.Log("INFO", "Window already exists (HWND=%x). Restoring to screen.", hwnd)
		procShowWindow.Call(uintptr(hwnd), SW_RESTORE)
		procSetForegroundWindow.Call(uintptr(hwnd))
		return
	}

	userDataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "VirtualWebcamSender", "edge_profile")
	_ = os.MkdirAll(userDataDir, 0755)

	browserExe := findBrowserExecutable()
	globalLogger.Log("INFO", "Starting webcam interface (%s, profile: %s)...", browserExe, userDataDir)
	args := []string{
		fmt.Sprintf("--app=%s", camURL),
		fmt.Sprintf("--user-data-dir=%s", userDataDir),
		"--window-size=1150,750",
		"--use-fake-ui-for-media-stream",
		"--ignore-certificate-errors",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--autoplay-policy=no-user-gesture-required",
	}
	cmd := exec.Command(browserExe, args...)
	if err := cmd.Start(); err != nil {
		globalLogger.Log("WARN", "Direct browser launch failed (%v), falling back to start command", err)
		cmdFallback := exec.Command("cmd", append([]string{"/c", "start", browserExe}, args...)...)
		cmdFallback.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = cmdFallback.Start()
	}
}

func main() {
	globalLogger = initLogger()
	defer globalLogger.RecoverPanic("main")

	systray.Run(onReady, onExit)
}

func onReady() {
	defer globalLogger.RecoverPanic("onReady")

	cfg := loadOrCreateConfig()
	globalLogger.Log("INFO", "Configuration loaded: CamURL=%s", cfg.CamURL)

	systray.SetIcon(iconData)
	systray.SetTitle("Webcam Sender")
	systray.SetTooltip("Webcam Sender - Active")

	mHeader := systray.AddMenuItem("📹 Webcam Sender", "Status")
	mHeader.Disable()

	mOpen := systray.AddMenuItem("👁 Minimize to Tray (-)", "Manage window visibility")
	mOpenGlobal = mOpen
	mPip := systray.AddMenuItemCheckbox("📌 Picture-in-Picture (Always on Top)", "Compact overlay always visible on top of Guacamole", false)

	systray.AddSeparator()

	mDevTitle := systray.AddMenuItem("🔌 Connected USB Video Devices:", "Device list")
	mDevTitle.Disable()

	devices := getConnectedVideoDevices()
	var devMenuItems []*systray.MenuItem
	if len(devices) == 0 {
		mNoDev := systray.AddMenuItem("  (No USB webcam detected)", "")
		mNoDev.Disable()
		devMenuItems = append(devMenuItems, mNoDev)
		globalLogger.Log("WARN", "No USB webcam detected at startup.")
	} else {
		for i, d := range devices {
			status := "Streaming"
			if i > 0 {
				status = "Available"
			}
			mDev := systray.AddMenuItem(fmt.Sprintf("  • 🟢 %s (%s)", d, status), "Local video device")
			mDev.Disable()
			devMenuItems = append(devMenuItems, mDev)
			globalLogger.Log("INFO", "Video device detected: %s [%s]", d, status)
		}
	}

	mRefresh := systray.AddMenuItem("🔄 Refresh device list", "Scan USB ports")

	systray.AddSeparator()

	isAuto := isAutostartEnabled()
	mAutostart := systray.AddMenuItemCheckbox("Start automatically with Windows", "Run on Windows startup", isAuto)

	mOpenLog := systray.AddMenuItem("📄 Open Debug Log", "View diagnostic log in Notepad")
	mOpenConfig := systray.AddMenuItem("Open Configuration Folder", "Edit Relay URL")

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Exit Webcam Sender", "Close application")

	// Open webcam window on startup
	openCamWindow(cfg.CamURL)

	// GOROUTINE: Hook for Tray Left-Click and Left Double-Click
	go func() {
		defer globalLogger.RecoverPanic("hookSystrayWindow")
		for i := 0; i < 40; i++ {
			time.Sleep(100 * time.Millisecond)
			clsName, _ := syscall.UTF16PtrFromString("SystrayClass")
			trayHwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(clsName)), 0)
			if trayHwnd != 0 {
				cb := syscall.NewCallback(func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
					if msg == 0x0401 { // WM_USER + 1: Shell_NotifyIcon callback
						switch lParam {
						case 0x0202, 0x0203: // WM_LBUTTONUP, WM_LBUTTONDBLCLK: left click or double-click
							globalLogger.Log("INFO", "Intercepted click/double-click on tray icon (lParam=0x%x). Restoring window...", lParam)
							restoreOrOpenCamWindow(cfg.CamURL)
							return 0
						case 0x0205: // WM_RBUTTONUP: right click -> show context menu
							ret, _, _ := procCallWindowProcW.Call(origWndProc, uintptr(hwnd), uintptr(msg), wParam, lParam)
							return ret
						}
					}
					ret, _, _ := procCallWindowProcW.Call(origWndProc, uintptr(hwnd), uintptr(msg), wParam, lParam)
					return ret
				})
				ret, _, _ := procSetWindowLongPtrW.Call(trayHwnd, GWLP_WNDPROC, cb)
				if ret != 0 {
					origWndProc = ret
					globalLogger.Log("INFO", "Tray icon subclassing completed successfully (HWND=%x). Click and double-click active!", trayHwnd)
					return
				}
			}
		}
	}()

	// GOROUTINE: Watch window minimize (-) to hide window into tray
	var lastHwnd syscall.Handle
	go func() {
		defer globalLogger.RecoverPanic("windowWatcher")
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		for range ticker.C {
			if lastHwnd == 0 || func() bool {
				ret, _, _ := procIsWindow.Call(uintptr(lastHwnd))
				return ret == 0
			}() {
				lastHwnd = findCamWindow()
			}

			if lastHwnd != 0 {
				isMin, _, _ := procIsIconic.Call(uintptr(lastHwnd))
				if isMin != 0 && !isHiddenGlobal {
					procShowWindow.Call(uintptr(lastHwnd), SW_HIDE)
					isHiddenGlobal = true
					mOpen.SetTitle("🪟 Show Webcam Window")
					systray.SetTooltip("Webcam Sender: Running in tray (Streaming active)")
					globalLogger.Log("INFO", "Window minimized to tray by user (-) - Streaming active.")
				}
			}
		}
	}()

	// GOROUTINE: Menu events
	go func() {
		defer globalLogger.RecoverPanic("systrayEvents")
		for {
			select {
			case <-mOpen.ClickedCh:
				if isHiddenGlobal {
					restoreOrOpenCamWindow(cfg.CamURL)
				} else {
					if lastHwnd == 0 || func() bool {
						ret, _, _ := procIsWindow.Call(uintptr(lastHwnd))
						return ret == 0
					}() {
						lastHwnd = findCamWindow()
					}
					if lastHwnd != 0 {
						procShowWindow.Call(uintptr(lastHwnd), SW_HIDE)
						isHiddenGlobal = true
						mOpen.SetTitle("🪟 Show Webcam Window")
						systray.SetTooltip("Webcam Sender: Running in tray (Streaming active)")
						globalLogger.Log("INFO", "Window hidden to tray from menu.")
					} else {
						restoreOrOpenCamWindow(cfg.CamURL)
					}
				}

			case <-mPip.ClickedCh:
				if lastHwnd == 0 || func() bool {
					ret, _, _ := procIsWindow.Call(uintptr(lastHwnd))
					return ret == 0
				}() {
					lastHwnd = findCamWindow()
				}
				if lastHwnd != 0 {
					if mPip.Checked() {
						procSetWindowPos.Call(uintptr(lastHwnd), uintptr(HWND_NOTOPMOST), 100, 100, 920, 720, SWP_SHOWWINDOW)
						mPip.Uncheck()
						globalLogger.Log("INFO", "Window restored to normal size.")
					} else {
						scrW, _, _ := procGetSystemMetrics.Call(uintptr(SM_CXSCREEN))
						scrH, _, _ := procGetSystemMetrics.Call(uintptr(SM_CYSCREEN))
						if scrW == 0 {
							scrW = 1920
						}
						if scrH == 0 {
							scrH = 1080
						}
						winW := uintptr(360)
						winH := uintptr(260)
						posX := scrW - winW - 30
						posY := scrH - winH - 60
						procShowWindow.Call(uintptr(lastHwnd), SW_RESTORE)
						procSetWindowPos.Call(uintptr(lastHwnd), uintptr(HWND_TOPMOST), posX, posY, winW, winH, SWP_SHOWWINDOW)
						procSetForegroundWindow.Call(uintptr(lastHwnd))
						mPip.Check()
						globalLogger.Log("INFO", "Window set to Picture-in-Picture (Always on Top, X=%d, Y=%d).", posX, posY)
					}
				}

			case <-mRefresh.ClickedCh:
				devs := getConnectedVideoDevices()
				globalLogger.Log("INFO", "Device list refreshed: %d found", len(devs))
				if len(devs) > 0 {
					for i, m := range devMenuItems {
						if i < len(devs) {
							m.SetTitle(fmt.Sprintf("  • 🟢 %s", devs[i]))
						}
					}
				}

			case <-mAutostart.ClickedCh:
				if mAutostart.Checked() {
					toggleAutostart(false)
					mAutostart.Uncheck()
				} else {
					toggleAutostart(true)
					mAutostart.Check()
				}

			case <-mOpenLog.ClickedCh:
				if globalLogger != nil && globalLogger.path != "" {
					_ = exec.Command("notepad.exe", globalLogger.path).Start()
				}

			case <-mOpenConfig.ClickedCh:
				dir := filepath.Dir(getConfigPath())
				_ = exec.Command("explorer", dir).Start()

			case <-mQuit.ClickedCh:
				globalLogger.Log("INFO", "Webcam Sender exit requested by user.")
				if lastHwnd != 0 {
					procShowWindow.Call(uintptr(lastHwnd), SW_RESTORE)
				}
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
}
