package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// In-memory listener: this sandbox (and some CI sandboxes) deny TCP bind
// entirely, so the SSE behaviour tests below drive the production
// http.Server through net.Pipe connections instead of a real socket.
// ---------------------------------------------------------------------------

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// pipeListener is a net.Listener that hands out the server side of
// net.Pipe connections created by dial. It touches no real socket.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{
		conns:  make(chan net.Conn),
		closed: make(chan struct{}),
	}
}

// dial returns the client side of a new connection and hands the server side
// to the listener (and therefore to http.Server.Serve).
func (l *pipeListener) dial(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	select {
	case l.conns <- server:
	case <-l.closed:
		client.Close()
		server.Close()
		t.Fatal("listener closed")
	}
	// Give the server a moment to pick the connection up and start serving.
	time.Sleep(20 * time.Millisecond)
	return client
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

// serveSSEOnPipe starts the production SSE server (built by newSSEServer) on
// an in-memory listener and returns it plus a shutdown func mirroring what
// Start's shutdown sequence does: cancel the base context, bounded Shutdown,
// Close on error.
func serveSSEOnPipe(t *testing.T, d *Daemon) (*pipeListener, func()) {
	t.Helper()
	srv := d.newSSEServer()
	ln := newPipeListener()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("sse server: %v", err)
		}
	}()
	return ln, func() {
		if d.sseBaseCancel != nil {
			d.sseBaseCancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}
}

// TestNewAppliesDefaultSSETimeouts: an Options with a zero SSETimeouts gets
// the Default* constants, and the server built from them carries those
// values.
func TestNewAppliesDefaultSSETimeouts(t *testing.T) {
	d := New(Options{})
	if d.sseTimeouts.ReadHeader != DefaultReadHeaderTimeout ||
		d.sseTimeouts.Read != DefaultReadTimeout ||
		d.sseTimeouts.Idle != DefaultIdleTimeout {
		t.Fatalf("zero SSETimeouts did not resolve to defaults: %+v", d.sseTimeouts)
	}
	srv := d.newSSEServer()
	if srv.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, DefaultReadHeaderTimeout)
	}
	if srv.ReadTimeout != DefaultReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", srv.ReadTimeout, DefaultReadTimeout)
	}
	if srv.ReadTimeout == 0 {
		t.Error("ReadTimeout must not be left unset")
	}
	// The trap: a server-wide WriteTimeout would kill long-lived streams.
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (per-stream deadlines in ServeSSE instead)", srv.WriteTimeout)
	}
	if srv.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", srv.IdleTimeout, DefaultIdleTimeout)
	}
	if srv.BaseContext == nil {
		t.Error("SSE server must have a BaseContext for prompt stream shutdown")
	}
	if srv.BaseContext != nil {
		// The daemon's BaseContext ignores the listener, so no real
		// listener (and no socket bind) is needed here.
		base := srv.BaseContext(nil)
		if base == nil || base.Err() != nil {
			t.Error("BaseContext must be a live (not cancelled) context")
		}
		if d.sseBaseCancel == nil {
			t.Error("newSSEServer must store the base cancel func")
		}
	}
	if srv.Addr == "" || srv.Handler == nil {
		t.Error("Addr and Handler must be set")
	}
}

// TestNewHonoursExplicitSSETimeouts: non-zero SSETimeouts fields are used as
// given (only zero fields fall back to the defaults).
func TestNewHonoursExplicitSSETimeouts(t *testing.T) {
	d := New(Options{SSETimeouts: SSETimeouts{ReadHeader: 250 * time.Millisecond}})
	srv := d.newSSEServer()
	if srv.ReadHeaderTimeout != 250*time.Millisecond {
		t.Errorf("ReadHeaderTimeout = %v, want 250ms", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != DefaultReadTimeout {
		t.Errorf("zero Read should default, got %v", srv.ReadTimeout)
	}
	if srv.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("zero Idle should default, got %v", srv.IdleTimeout)
	}
}

// TestSSEServeRejectsStalledPartialRequest: a client that sends half a
// request line and then stalls is disconnected by ReadHeaderTimeout — the
// connection ends on the server's initiative shortly after the timeout,
// instead of being held open forever.
func TestSSEServeRejectsStalledPartialRequest(t *testing.T) {
	const readHeader = 300 * time.Millisecond
	d := New(Options{SSETimeouts: SSETimeouts{ReadHeader: readHeader}})
	ln, shutdown := serveSSEOnPipe(t, d)
	defer shutdown()

	conn := ln.dial(t)
	defer func() { _ = conn.Close() }()

	// Send only half a request line, then sit on our hands.
	if _, err := conn.Write([]byte("GET /event")); err != nil {
		t.Fatalf("write partial request: %v", err)
	}

	start := time.Now()
	// Read until the server closes. The deadline is ~5x the header timeout:
	// hitting it means the stalled connection was never dropped.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(conn)
	elapsed := time.Since(start)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatalf("stalled partial request was never disconnected: no response after %v (ReadHeaderTimeout=%v)",
				elapsed, readHeader)
		}
		t.Fatalf("read from stalled partial request: %v", err)
	}
	if elapsed < readHeader/3 {
		t.Fatalf("connection ended after %v, suspiciously before ReadHeaderTimeout (%v): %q",
			elapsed, readHeader, got)
	}
	if elapsed > readHeader+1500*time.Millisecond {
		t.Fatalf("stalled partial request dropped only after %v; want close near ReadHeaderTimeout (%v)",
			elapsed, readHeader)
	}
	t.Logf("stalled partial request dropped after %v, server said %q", elapsed, got)
}

// TestSSEStreamSurvivesTimeoutsAndIsEndedByCancel: an /events stream stays
// alive (and receives events) long past every configured timeout, and ends
// promptly once the SSE base context is cancelled (what Start does at
// shutdown).
func TestSSEStreamSurvivesTimeoutsAndIsEndedByCancel(t *testing.T) {
	d := New(Options{
		SSETimeouts: SSETimeouts{
			ReadHeader: 100 * time.Millisecond,
			Read:       300 * time.Millisecond,
			Idle:       300 * time.Millisecond,
		},
	})
	ln, shutdown := serveSSEOnPipe(t, d)
	defer shutdown()

	conn := ln.dial(t)
	defer func() { _ = conn.Close() }()
	if _, err := fmt.Fprintf(conn, "GET /events HTTP/1.1\r\nHost: x\r\nAccept: text/event-stream\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Emit an event after the stream has been open longer than every
	// timeout; the stream must still be alive to receive it.
	go func() {
		time.Sleep(700 * time.Millisecond) // > Read, > Idle, > ReadHeader
		d.bus.Emit("", "keepalive", nil)
	}()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	line := make([]byte, 0, 128)
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, err := br.ReadByte()
		if err != nil {
			t.Fatalf("stream did not deliver the event issued after all timeouts expired: %v", err)
		}
		if b == '\n' {
			s := string(line)
			if s == "" {
				continue // blank SSE separator
			}
			line = line[:0]
			if len(s) > 5 && s[:5] == "data:" {
				break // event received well past every timeout
			}
		} else {
			line = append(line, b)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for SSE event")
		}
	}

	// Now simulate shutdown: cancelling the base context (what Start does)
	// must end the stream promptly, without the client closing first.
	d.sseBaseCancel()
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second)) // generous: well under 5s
	if _, err := io.ReadAll(conn); err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("open stream still alive 1s after base context cancel; shutdown must end it promptly")
		}
		t.Fatalf("stream did not end cleanly after base cancel: %v", err)
	}
}

// TestStartEndsOpenStreamsPromptlyOnCancel: with a full daemon running and an
// open /events stream, cancelling the Start context makes Start return well
// under a second and the stream end — the shutdown BaseContext path.
// Needs real socket binds, so it skips where those are denied.
func TestStartEndsOpenStreamsPromptlyOnCancel(t *testing.T) {
	requireTCPBindForTimeoutTests(t)
	requireUnixSocketsForTimeoutTests(t)
	dir := shortTempDir(t)
	d := New(Options{
		SocketPath: filepath.Join(dir, "ctl.sock"),
		PIDPath:    filepath.Join(dir, "d.pid"),
		SSEAddr:    fmt.Sprintf("127.0.0.1:%d", freeTCPPort(t)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()

	// Wait for the SSE port to accept.
	deadline := time.Now().Add(5 * time.Second)
	var conn net.Conn
	for {
		var err error
		conn, err = net.Dial("tcp", d.sseAddr)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon SSE server never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer func() { _ = conn.Close() }()
	if _, err := fmt.Fprintf(conn, "GET /events HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Confirm the stream is live before shutting down.
	go func() {
		time.Sleep(100 * time.Millisecond)
		d.bus.Emit("", "hello", nil)
	}()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(conn)
	if _, err := http.ReadResponse(br, nil); err != nil {
		t.Fatalf("read response: %v", err)
	}
	found := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := br.Read(buf)
			if n > 0 && bytes.Contains(buf[:n], []byte("data:")) {
				close(found)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-found:
	case <-time.After(3 * time.Second):
		t.Fatal("open stream never received an event")
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
		if el := time.Since(start); el > time.Second {
			t.Fatalf("Start took %v to return with an open SSE stream; want well under 1s", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
	// The open stream must end on its own (server closes it).
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(br); err != nil {
		t.Fatalf("stream did not end after shutdown: %v", err)
	}
}

// requireUnixSocketsForTimeoutTests skips the test if this environment cannot
// bind Unix sockets at all (some sandboxes deny AF_UNIX bind everywhere).
func requireUnixSocketsForTimeoutTests(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe.sock")
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Skipf("environment cannot bind unix sockets: %v", err)
	}
	_ = l.Close()
}

// requireTCPBindForTimeoutTests skips the test if this environment cannot
// bind a localhost TCP socket at all (some sandboxes deny bind entirely).
func requireTCPBindForTimeoutTests(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("environment cannot bind tcp sockets: %v", err)
	}
	_ = l.Close()
}
