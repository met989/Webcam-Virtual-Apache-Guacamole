package main

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed static/*
var embeddedStatic embed.FS

// Config holds runtime configuration
type Config struct {
	Port      string
	StaticDir string
	AuthToken string
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
}

// Client wraps a WebSocket connection with its metadata
type Client struct {
	hub         *Hub
	conn        *websocket.Conn
	room        string
	role        string // "sender" or "receiver"
	remoteAddr  string
	connectedAt time.Time
	sendChan    chan []byte
	isBinary    bool
}

// Room manages clients for a specific session/room
type Room struct {
	sync.RWMutex
	name        string
	sender      *Client
	receivers   map[*Client]bool
	framesSent  uint64
	bytesSent   uint64
	droppedCnt  uint64
	lastFrameAt time.Time
}

// Hub manages all active rooms
type Hub struct {
	sync.RWMutex
	rooms map[string]*Room
}

func newHub() *Hub {
	return &Hub{
		rooms: make(map[string]*Room),
	}
}

func (h *Hub) getOrCreateRoom(name string) *Room {
	h.Lock()
	defer h.Unlock()

	if room, exists := h.rooms[name]; exists {
		return room
	}

	room := &Room{
		name:      name,
		receivers: make(map[*Client]bool),
	}
	h.rooms[name] = room
	return room
}

func (r *Room) setSender(c *Client) {
	r.Lock()
	if r.sender != nil && r.sender != c {
		log.Printf("[%s] Replacing previous sender (%s)...", r.name, r.sender.remoteAddr)
		r.sender.conn.Close()
	}
	r.sender = c
	receiverCount := len(r.receivers)
	r.Unlock()

	log.Printf("[%s] Sender connected from %s. Waiting receivers: %d", r.name, c.remoteAddr, receiverCount)

	// Notify receivers that sender is online
	r.broadcastControlMessage(map[string]interface{}{
		"event":  "sender_online",
		"status": true,
	})

	if receiverCount > 0 {
		c.sendControlMessage(map[string]interface{}{
			"event":  "receiver_status",
			"online": true,
		})
		c.sendControlMessage(map[string]interface{}{
			"event": "request_keyframe",
		})
	} else {
		c.sendControlMessage(map[string]interface{}{
			"event":  "receiver_status",
			"online": false,
		})
	}
}

func (r *Room) removeSender(c *Client) {
	r.Lock()
	if r.sender == c {
		r.sender = nil
		log.Printf("[%s] Sender disconnected (%s)", r.name, c.remoteAddr)
	}
	r.Unlock()

	r.broadcastControlMessage(map[string]interface{}{
		"event":  "sender_online",
		"status": false,
	})
}

func (r *Room) addReceiver(c *Client) {
	r.Lock()
	r.receivers[c] = true
	hasSender := r.sender != nil
	total := len(r.receivers)
	r.Unlock()

	log.Printf("[%s] Receiver connected from %s. Total receivers: %d", r.name, c.remoteAddr, total)

	c.sendControlMessage(map[string]interface{}{
		"event":  "sender_online",
		"status": hasSender,
	})

	if hasSender {
		r.RLock()
		if r.sender != nil {
			r.sender.sendControlMessage(map[string]interface{}{
				"event":  "receiver_status",
				"online": true,
			})
			r.sender.sendControlMessage(map[string]interface{}{
				"event": "request_keyframe",
			})
		}
		r.RUnlock()
	}
}

func (r *Room) removeReceiver(c *Client) {
	r.Lock()
	delete(r.receivers, c)
	remaining := len(r.receivers)
	r.Unlock()
	log.Printf("[%s] Receiver disconnected (%s). Remaining: %d", r.name, c.remoteAddr, remaining)

	if remaining == 0 {
		r.RLock()
		if r.sender != nil {
			r.sender.sendControlMessage(map[string]interface{}{
				"event":  "receiver_status",
				"online": false,
			})
		}
		r.RUnlock()
	}
}

// Inoltra frame video o audio a tutti i ricevitori della stanza.
// Buffer limitato a 4 frame: se un ricevitore è temporaneamente lento,
// i frame obsoleti vengono scartati per garantire zero lag e zero scatti.
func (r *Room) broadcastMediaFrame(data []byte) {
	atomic.AddUint64(&r.framesSent, 1)
	atomic.AddUint64(&r.bytesSent, uint64(len(data)))
	r.lastFrameAt = time.Now()

	r.RLock()
	defer r.RUnlock()

	for receiver := range r.receivers {
		select {
		case receiver.sendChan <- data:
		default:
			atomic.AddUint64(&r.droppedCnt, 1)
			// Buffer pieno: scarta per mantenere latenza zero e prevenire buffering
		}
	}
}

func (r *Room) broadcastControlMessage(msg map[string]interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	r.RLock()
	defer r.RUnlock()

	for receiver := range r.receivers {
		select {
		case receiver.sendChan <- data:
		default:
		}
	}
}

func (c *Client) sendControlMessage(msg map[string]interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case c.sendChan <- data:
	default:
	}
}

func (c *Client) readPump(room *Room) {
	defer func() {
		if c.role == "sender" {
			room.removeSender(c)
		} else {
			room.removeReceiver(c)
		}
		c.conn.Close()
	}()

	c.conn.SetReadLimit(5 * 1024 * 1024) // 5 MB max packet size
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		msgType, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[%s:%s] WebSocket read error (%s): %v", c.room, c.role, c.remoteAddr, err)
			}
			break
		}

		// IMPORTANTE: Resetta il read deadline ad ogni pacchetto ricevuto con successo!
		// Previene il timeout del socket durante la trasmissione attiva.
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		if c.role == "sender" {
			if msgType == websocket.BinaryMessage {
				// Pacchetto multimediale (VCAM video o ACAM audio per microfono)
				room.broadcastMediaFrame(message)
			}
		} else {
			if msgType == websocket.TextMessage {
				var ctrl map[string]interface{}
				if err := json.Unmarshal(message, &ctrl); err == nil {
					if ctrl["type"] == "request_keyframe" || ctrl["event"] == "request_keyframe" {
						room.RLock()
						if room.sender != nil {
							room.sender.sendControlMessage(map[string]interface{}{
								"event": "request_keyframe",
							})
						}
						room.RUnlock()
					}
				}
			}
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(20 * time.Second) // Ping ogni 20 secondi
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case data, ok := <-c.sendChan:
			c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			msgType := websocket.BinaryMessage
			if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
				msgType = websocket.TextMessage
			}

			if err := c.conn.WriteMessage(msgType, data); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func handleWebSocket(hub *Hub, cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		role := strings.ToLower(q.Get("role"))
		roomName := q.Get("room")
		token := q.Get("token")

		if role != "sender" && role != "receiver" {
			http.Error(w, "Missing or invalid 'role' parameter ('sender' or 'receiver')", http.StatusBadRequest)
			return
		}

		if roomName == "" {
			roomName = "default"
		}

		if cfg.AuthToken != "" && token != cfg.AuthToken {
			http.Error(w, "Invalid authentication token", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WebSocket upgrade failed: %v", err)
			return
		}

		client := &Client{
			hub:         hub,
			conn:        conn,
			room:        roomName,
			role:        role,
			remoteAddr:  r.RemoteAddr,
			connectedAt: time.Now(),
			// Buffer ultracompatto di 4 frame: previene accumulo di latenza e scatti
			sendChan: make(chan []byte, 4),
		}

		room := hub.getOrCreateRoom(roomName)

		if role == "sender" {
			room.setSender(client)
		} else {
			room.addReceiver(client)
		}

		go client.writePump()
		go client.readPump(room)
	}
}

var startTime = time.Now()

func handleStatus(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		hub.RLock()
		defer hub.RUnlock()

		roomsData := make(map[string]interface{})
		for name, room := range hub.rooms {
			room.RLock()
			senderOnline := room.sender != nil
			var senderAddr string
			if senderOnline {
				senderAddr = room.sender.remoteAddr
			}
			recvCount := len(room.receivers)
			frames := atomic.LoadUint64(&room.framesSent)
			bytes := atomic.LoadUint64(&room.bytesSent)
			dropped := atomic.LoadUint64(&room.droppedCnt)

			roomsData[name] = map[string]interface{}{
				"sender_online":  senderOnline,
				"sender_ip":      senderAddr,
				"receivers":      recvCount,
				"frames_total":   frames,
				"bytes_total":    bytes,
				"frames_dropped": dropped,
				"last_frame":     room.lastFrameAt.Format(time.RFC3339),
			}
			room.RUnlock()
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "ok",
			"uptime":   time.Since(startTime).String(),
			"started":  startTime.Format(time.RFC3339),
			"rooms":    roomsData,
		})
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9090"
	}

	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "./static"
	}

	authToken := os.Getenv("AUTH_TOKEN")

	// Configurazione logging su file e su stdout
	var logWriters []io.Writer
	logWriters = append(logWriters, os.Stdout)

	logFilePath := "/var/log/virtualcam-relay.log"
	f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	if err != nil {
		// Fallback su cartella locale
		f, _ = os.OpenFile("virtualcam-relay.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	}
	if f != nil {
		defer f.Close()
		logWriters = append(logWriters, f)
	}
	log.SetOutput(io.MultiWriter(logWriters...))
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	cfg := Config{
		Port:      port,
		StaticDir: staticDir,
		AuthToken: authToken,
	}

	hub := newHub()
	mux := http.NewServeMux()

	// Healthcheck & Diagnostica real-time
	statusHandler := handleStatus(hub)
	mux.HandleFunc("/health", statusHandler)
	mux.HandleFunc("/cam/health", statusHandler)
	mux.HandleFunc("/status", statusHandler)
	mux.HandleFunc("/cam/status", statusHandler)

	// WebSocket endpoints
	wsHandler := handleWebSocket(hub, cfg)
	mux.HandleFunc("/ws", wsHandler)
	mux.HandleFunc("/cam/ws", wsHandler)

	// File statici Web Sender
	var staticHandler http.Handler
	if _, err := os.Stat(staticDir); err == nil {
		absStaticDir, _ := filepath.Abs(staticDir)
		log.Printf("Using static directory on disk: %s", absStaticDir)
		staticHandler = http.FileServer(http.Dir(absStaticDir))
	} else {
		log.Printf("Using embedded static directory")
		subFS, err := fs.Sub(embeddedStatic, "static")
		if err != nil {
			log.Fatalf("Error loading embedded static directory: %v", err)
		}
		staticHandler = http.FileServer(http.FS(subFS))
	}

	mux.Handle("/", staticHandler)
	mux.Handle("/cam/", http.StripPrefix("/cam", staticHandler))

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	log.Printf("==================================================")
	log.Printf(" VirtualCam Relay Server started on :%s", port)
	log.Printf(" Web Sender: https://<ip>/cam/")
	log.Printf(" Status & Diagnostics: https://<ip>/cam/status")
	log.Printf("==================================================")

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Fatal server error: %v", err)
	}
}
