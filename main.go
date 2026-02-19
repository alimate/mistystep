package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"embed"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/mdns"
)

//go:embed static
var staticFS embed.FS

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type config struct {
	port      int
	bind      string
	dir       string        // upload / browse directory
	mdnsName  string        // mDNS service instance name
	mdnsHost  string        // hostname used for .local
	noMDNS    bool          // skip mDNS registration
	clipPoll  time.Duration // clipboard poll interval
	filePoll  time.Duration // file-list poll interval
	maxUpload int64         // max upload body in bytes
}

// ---------------------------------------------------------------------------
// Data types
// ---------------------------------------------------------------------------

type FileEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type Message struct {
	Type    string      `json:"type"`
	Content string      `json:"content,omitempty"`
	Files   []FileEntry `json:"files,omitempty"`
}

// ---------------------------------------------------------------------------
// WebSocket Hub
// ---------------------------------------------------------------------------

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

type Hub struct {
	mu         sync.RWMutex
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	broadcast  chan []byte
}

func newHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan []byte, 64),
	}
}

func (h *Hub) run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = true
			h.mu.Unlock()

		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()

		case msg := <-h.broadcast:
			h.mu.RLock()
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					// slow client — drop and close
					close(c.send)
					delete(h.clients, c)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) sendTo(c *Client, msg []byte) {
	select {
	case c.send <- msg:
	default:
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (c *Client) writePump() {
	defer c.conn.Close()
	for msg := range c.send {
		c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Pollers
// ---------------------------------------------------------------------------

func marshalMsg(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func pollClipboard(h *Hub, interval time.Duration) {
	var lastHash uint64
	for {
		time.Sleep(interval)
		text, err := GetClipboard()
		if err != nil {
			continue
		}
		hash := fnvHash([]byte(text))
		if hash == lastHash {
			continue
		}
		lastHash = hash
		h.broadcast <- marshalMsg(Message{Type: "clipboard", Content: text})
	}
}

func fileEntries(dir string) ([]FileEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []FileEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, FileEntry{
			Name:     e.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().Unix(),
		})
	}
	return files, nil
}

func pollFiles(h *Hub, dir string, interval time.Duration) {
	var lastHash uint64
	for {
		time.Sleep(interval)
		files, err := fileEntries(dir)
		if err != nil {
			continue
		}
		hash := hashFiles(files)
		if hash == lastHash {
			continue
		}
		lastHash = hash
		h.broadcast <- marshalMsg(Message{Type: "files", Files: files})
	}
}

func fnvHash(data []byte) uint64 {
	h := fnv.New64a()
	h.Write(data)
	return h.Sum64()
}

func hashFiles(files []FileEntry) uint64 {
	h := fnv.New64a()
	for _, f := range files {
		fmt.Fprintf(h, "%s|%d|%d\n", f.Name, f.Size, f.Modified)
	}
	return h.Sum64()
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// server bundles the config needed by HTTP handlers.
type server struct {
	cfg *config
	hub *Hub
}

func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	c := &Client{hub: s.hub, conn: conn, send: make(chan []byte, 16)}
	s.hub.register <- c
	go c.writePump()
	go c.readPump()

	// push current state immediately on connect
	if text, err := GetClipboard(); err == nil {
		s.hub.sendTo(c, marshalMsg(Message{Type: "clipboard", Content: text}))
	}
	if files, err := fileEntries(s.cfg.dir); err == nil {
		s.hub.sendTo(c, marshalMsg(Message{Type: "files", Files: files}))
	}
}

func (s *server) handleClipboardSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if err := SetClipboard(string(body)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleFileList(w http.ResponseWriter, r *http.Request) {
	files, err := fileEntries(s.cfg.dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if files == nil {
		files = []FileEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

func (s *server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	rawName := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if rawName == "" {
		s.handleFileList(w, r)
		return
	}
	clean := filepath.Base(filepath.Clean(rawName))
	full := filepath.Join(s.cfg.dir, clean)
	if !strings.HasPrefix(full, s.cfg.dir+string(filepath.Separator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "stat error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, clean))
	http.ServeContent(w, r, clean, info.ModTime(), f)
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.maxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "parse error: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	clean := filepath.Base(filepath.Clean(header.Filename))
	dest := filepath.Join(s.cfg.dir, clean)
	if !strings.HasPrefix(dest, s.cfg.dir+string(filepath.Separator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	out, err := os.Create(dest)
	if err != nil {
		http.Error(w, "create error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		http.Error(w, "write error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// mDNS
// ---------------------------------------------------------------------------

func startMDNS(cfg *config) {
	svc, err := mdns.NewMDNSService(
		cfg.mdnsName, "_http._tcp", "local.", cfg.mdnsHost+".",
		cfg.port, nil, []string{"path=/"},
	)
	if err != nil {
		log.Printf("mDNS service create: %v (continuing without mDNS)", err)
		return
	}
	if _, err = mdns.NewServer(&mdns.Config{Zone: svc}); err != nil {
		log.Printf("mDNS server: %v — avahi may already hold UDP 5353; .local resolution still works via avahi", err)
		return
	}
	log.Printf("mDNS: advertising http://%s.local:%d  (name=%s)", cfg.mdnsHost, cfg.port, cfg.mdnsName)
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	home, _ := os.UserHomeDir()

	var (
		portFlag      = flag.Int("port", 7070, "HTTP listen port")
		bindFlag      = flag.String("bind", "", "Listen address (empty = all interfaces)")
		dirFlag       = flag.String("dir", filepath.Join(home, "Downloads"), "Directory for uploads and file browser")
		mdnsNameFlag  = flag.String("mdns-name", "mistystep", "mDNS service instance name")
		mdnsHostFlag  = flag.String("mdns-host", "", "Hostname for .local mDNS (default: os.Hostname())")
		noMDNSFlag    = flag.Bool("no-mdns", false, "Disable mDNS registration")
		clipPollFlag  = flag.Duration("clip-poll", 1*time.Second, "Clipboard poll interval")
		filePollFlag  = flag.Duration("file-poll", 3*time.Second, "File list poll interval")
		maxUploadFlag = flag.Int("max-upload", 500, "Max upload size in MiB")
	)
	flag.Parse()

	// Resolve mdns-host default: use the machine hostname so avahi's
	// existing .local resolution works (avahi typically holds UDP 5353).
	mdnsHost := *mdnsHostFlag
	if mdnsHost == "" {
		mdnsHost, _ = os.Hostname()
	}

	// Expand ~ in dir path
	dir := *dirFlag
	if strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(home, dir[2:])
	}
	dir = filepath.Clean(dir)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatalf("cannot create directory %q: %v", dir, err)
	}

	cfg := &config{
		port:      *portFlag,
		bind:      *bindFlag,
		dir:       dir,
		mdnsName:  *mdnsNameFlag,
		mdnsHost:  mdnsHost,
		noMDNS:    *noMDNSFlag,
		clipPoll:  *clipPollFlag,
		filePoll:  *filePollFlag,
		maxUpload: int64(*maxUploadFlag) << 20,
	}

	h := newHub()
	go h.run()
	go pollClipboard(h, cfg.clipPoll)
	go pollFiles(h, cfg.dir, cfg.filePoll)

	if !cfg.noMDNS {
		go startMDNS(cfg)
	}

	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("embed sub: %v", err)
	}

	srv := &server{cfg: cfg, hub: h}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(subFS)))
	mux.HandleFunc("/ws", srv.handleWS)
	mux.HandleFunc("/api/clipboard", srv.handleClipboardSet)
	mux.HandleFunc("/api/files", srv.handleFileList)
	mux.HandleFunc("/api/files/", srv.handleFileDownload)
	mux.HandleFunc("/api/upload", srv.handleUpload)

	addr := fmt.Sprintf("%s:%d", cfg.bind, cfg.port)
	log.Printf("dir:          %s", cfg.dir)
	log.Printf("clip-poll:    %s   file-poll: %s   max-upload: %d MiB",
		cfg.clipPoll, cfg.filePoll, *maxUploadFlag)
	log.Printf("listening on  %s", addr)
	log.Printf("local:        http://localhost:%d", cfg.port)
	if ip := lanIP(); ip != "" {
		log.Printf("network:      http://%s:%d", ip, cfg.port)
	}
	if !cfg.noMDNS {
		log.Printf("mdns:         http://%s.local:%d", cfg.mdnsHost, cfg.port)
	}

	log.Fatal(http.ListenAndServe(addr, mux))
}

func lanIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
