package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/yourusername/repomap-go/internal/events"
	"github.com/yourusername/repomap-go/internal/mcp"
	"github.com/yourusername/repomap-go/internal/project"
)

const (
	DefaultSocketPath = "/tmp/repomap-daemon.sock"
	DefaultPIDPath    = "/tmp/repomap-daemon.pid"
	DefaultSSEAddr    = "127.0.0.1:7374"
)

// Message is the JSON envelope sent over the control socket.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Response is the reply envelope.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Daemon owns the lifecycle of all projects and the control + SSE servers.
type Daemon struct {
	mu       sync.RWMutex
	projects map[string]*project.Project

	bus *events.Bus

	socketPath string
	pidPath    string
	sseAddr    string

	listener  net.Listener
	sseServer *http.Server

	startedAt time.Time
}

// Options control the daemon.
type Options struct {
	SocketPath string
	PIDPath    string
	SSEAddr    string
}

// New creates a Daemon ready to Start.
func New(opts Options) *Daemon {
	if opts.SocketPath == "" {
		opts.SocketPath = DefaultSocketPath
	}
	if opts.PIDPath == "" {
		opts.PIDPath = DefaultPIDPath
	}
	if opts.SSEAddr == "" {
		opts.SSEAddr = DefaultSSEAddr
	}
	return &Daemon{
		projects:   make(map[string]*project.Project),
		bus:        events.NewBus(),
		socketPath: opts.SocketPath,
		pidPath:    opts.PIDPath,
		sseAddr:    opts.SSEAddr,
	}
}

// Start runs the daemon until ctx is cancelled (or a signal is received).
func (d *Daemon) Start(ctx context.Context) error {
	d.startedAt = time.Now()
	if err := d.writePID(); err != nil {
		return err
	}
	defer os.Remove(d.pidPath)

	_ = os.Remove(d.socketPath)
	l, err := net.Listen("unix", d.socketPath)
	if err != nil {
		return fmt.Errorf("listen control socket: %w", err)
	}
	if err := os.Chmod(d.socketPath, 0o600); err != nil {
		l.Close()
		return fmt.Errorf("chmod control socket: %w", err)
	}
	d.listener = l
	defer func() {
		l.Close()
		os.Remove(d.socketPath)
	}()

	// SSE server.
	mux := http.NewServeMux()
	mux.HandleFunc("/events", d.bus.ServeSSE)
	d.sseServer = &http.Server{
		Addr:    d.sseAddr,
		Handler: mux,
	}
	go func() {
		if err := d.sseServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// surface via stderr
			fmt.Fprintf(os.Stderr, "sse server: %v\n", err)
		}
	}()

	// Signal handling.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Accept loop.
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go d.handleConn(ctx, conn)
		}
	}()

	<-ctx.Done()

	// Shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = d.sseServer.Shutdown(shutdownCtx)

	d.mu.Lock()
	for _, p := range d.projects {
		_ = p.Stop()
	}
	d.projects = make(map[string]*project.Project)
	d.mu.Unlock()
	return nil
}

func (d *Daemon) writePID() error {
	// If a pid file exists and the process is alive, refuse to start.
	if data, err := os.ReadFile(d.pidPath); err == nil {
		var pid int
		fmt.Sscanf(string(data), "%d", &pid)
		if pid > 0 && processAlive(pid) {
			return fmt.Errorf("daemon already running (pid %d)", pid)
		}
	}
	pid := os.Getpid()
	return os.WriteFile(d.pidPath, []byte(fmt.Sprintf("%d\n", pid)), 0o644)
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0: existence check.
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	var msg Message
	if err := dec.Decode(&msg); err != nil {
		if err != io.EOF {
			_ = enc.Encode(Response{OK: false, Error: err.Error()})
		}
		return
	}
	resp := d.dispatch(ctx, msg)
	_ = enc.Encode(resp)
}

func (d *Daemon) dispatch(ctx context.Context, msg Message) Response {
	switch msg.Type {
	case "add":
		var payload struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return errResp(err)
		}
		if err := d.AddProject(ctx, payload.Path); err != nil {
			return errResp(err)
		}
		return okResp(map[string]string{"added": payload.Path})

	case "remove":
		var payload struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return errResp(err)
		}
		if err := d.RemoveProject(payload.Path); err != nil {
			return errResp(err)
		}
		return okResp(map[string]string{"removed": payload.Path})

	case "list":
		return okResp(d.List())

	case "status":
		return okResp(d.Status())

	case "mcp_connect":
		var payload struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return errResp(err)
		}
		abs, err := filepath.Abs(payload.Path)
		if err != nil {
			return errResp(err)
		}
		d.mu.RLock()
		p, ok := d.projects[abs]
		d.mu.RUnlock()
		if !ok {
			// Auto-register on first mcp_connect.
			if err := d.AddProject(ctx, abs); err != nil {
				return errResp(fmt.Errorf("auto-register: %w", err))
			}
			d.mu.RLock()
			p, ok = d.projects[abs]
			d.mu.RUnlock()
			if !ok {
				return errResp(fmt.Errorf("project missing after add: %s", abs))
			}
		}
		// Wait briefly for the per-project socket to be ready on disk.
		sockPath := p.SocketPath()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, statErr := os.Stat(sockPath); statErr == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		return okResp(map[string]string{"socket": sockPath, "socket_path": sockPath})

	case "shutdown":
		go func() {
			time.Sleep(50 * time.Millisecond)
			proc, _ := os.FindProcess(os.Getpid())
			_ = proc.Signal(syscall.SIGTERM)
		}()
		return okResp(map[string]string{"status": "shutting down"})
	}
	return errResp(fmt.Errorf("unknown message type: %q", msg.Type))
}

// AddProject registers and starts a project.
func (d *Daemon) AddProject(ctx context.Context, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if _, exists := d.projects[abs]; exists {
		d.mu.Unlock()
		return fmt.Errorf("already registered: %s", abs)
	}
	d.mu.Unlock()

	p, err := project.New(abs, d.bus)
	if err != nil {
		return err
	}
	// Wire MCP server for this project before starting the listener so the
	// per-project socket accepts connections with a real handler attached.
	mcpServer := mcp.New(abs, p)
	p.SetMCPHandler(mcpServer.HandleConn)
	if err := p.Start(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	d.projects[abs] = p
	d.mu.Unlock()
	return nil
}

// RemoveProject stops and unregisters a project.
func (d *Daemon) RemoveProject(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	d.mu.Lock()
	p, ok := d.projects[abs]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("not registered: %s", abs)
	}
	delete(d.projects, abs)
	d.mu.Unlock()
	return p.Stop()
}

// List returns a slice of project snapshots.
func (d *Daemon) List() []map[string]interface{} {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]map[string]interface{}, 0, len(d.projects))
	for _, p := range d.projects {
		out = append(out, p.Snapshot())
	}
	return out
}

// Status returns daemon-wide status.
func (d *Daemon) Status() map[string]interface{} {
	d.mu.RLock()
	defer d.mu.RUnlock()
	projects := make([]map[string]interface{}, 0, len(d.projects))
	var totalTags int
	var totalBytes int64
	for _, p := range d.projects {
		s := p.Snapshot()
		projects = append(projects, s)
		if t, ok := s["tags"].(int); ok {
			totalTags += t
		}
		if b, ok := s["memory_bytes"].(int64); ok {
			totalBytes += b
		}
	}
	return map[string]interface{}{
		"pid":              os.Getpid(),
		"uptime_seconds":   int(time.Since(d.startedAt).Seconds()),
		"projects":         projects,
		"project_count":    len(d.projects),
		"total_tags":       totalTags,
		"memory_bytes":     totalBytes,
		"control_socket":   d.socketPath,
		"sse_addr":         d.sseAddr,
	}
}

func okResp(v interface{}) Response {
	data, _ := json.Marshal(v)
	return Response{OK: true, Data: data}
}

func errResp(err error) Response {
	return Response{OK: false, Error: err.Error()}
}

// --- Client helpers ----------------------------------------------------------

// SendMessage opens the control socket, sends a message, and returns the response.
func SendMessage(socketPath string, msg Message) (Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(msg); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// IsRunning checks whether a daemon is running at socketPath/pidPath.
func IsRunning(pidPath string) (int, bool) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, false
	}
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	if pid <= 0 {
		return 0, false
	}
	return pid, processAlive(pid)
}
