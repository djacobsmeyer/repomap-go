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

// The SSE timeout tests below drive the production http.Server (built by
// newSSEServer) through net.Pipe connections, so they need no socket bind.
// Only TestStartEndsOpenStreamsPromptlyOnCancel runs a real daemon, and
// requireBind skips it where binds are denied.

// In-memory listener: a net.Listener that hands out the server side of
// net.Pipe connections, touching no real socket.

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

// dial returns the client side of a new connection and hands the server side
// to the listener (and therefore to http.Server.Serve). net.Pipe is
// unbuffered, so client writes block until the server reads them: no sleep
// needed for the server to pick the connection up.
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

// serveSSEOnPipe starts the production SSE server on an in-memory listener
// and returns it plus a shutdown func mirroring Start's shutdown steps:
// cancel the base context, bounded Shutdown, Close on error.
func serveSSEOnPipe(t *testing.T, d *Daemon) (*pipeListener, func()) {
	t.Helper()
	srv := d.newSSEServer()
	ln := &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("sse server: %v", err)
		}
	}()
	return ln, func() {
		d.sseBaseCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}
}

// Raw-HTTP client helpers.

// getStream sends a GET request on conn and returns a reader positioned on
// the response body, asserting status 200. ServeSSE flushes its headers with
// the first event, so the caller must have subscribed (with an event coming)
// before this returns.
func getStream(t *testing.T, conn net.Conn, path string, within time.Duration) *bufio.Reader {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(within))
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\nAccept: text/event-stream\r\n\r\n", path); err != nil {
		t.Fatalf("write request: %v", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	return br
}

// expectData fails the test unless an SSE "data:" payload arrives on br
// within the given window. It scans raw bytes because the body is chunked, so
// a line-based read could straddle a chunk boundary.
func expectData(t *testing.T, conn net.Conn, br *bufio.Reader, within time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(within))
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		if bytes.Contains(buf[:n], []byte("data:")) {
			return
		}
		if err != nil {
			t.Fatalf("no SSE event within %v: %v", within, err)
		}
	}
}

// TestNewResolvesSSETimeouts: zero SSETimeouts fields resolve to the Default*
// constants, non-zero fields are honoured, and the server built from them
// carries the result.
func TestNewResolvesSSETimeouts(t *testing.T) {
	// Defaults.
	srv := New(Options{}).newSSEServer()
	if srv.ReadHeaderTimeout != DefaultReadHeaderTimeout || srv.ReadTimeout != DefaultReadTimeout || srv.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("default timeouts = %v/%v/%v, want %v/%v/%v", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout,
			DefaultReadHeaderTimeout, DefaultReadTimeout, DefaultIdleTimeout)
	}
	// The trap: a server-wide WriteTimeout would kill long-lived streams.
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (ServeSSE sets per-write deadlines instead)", srv.WriteTimeout)
	}
	if srv.Addr == "" || srv.Handler == nil {
		t.Error("Addr and Handler must be set")
	}
	if srv.BaseContext == nil {
		t.Fatal("SSE server must have a BaseContext for prompt stream shutdown")
	}
	if base := srv.BaseContext(nil); base == nil || base.Err() != nil {
		t.Error("BaseContext must return a live (not cancelled) context")
	}

	// Explicit value honoured; the untouched zero fields still default.
	srv = New(Options{SSETimeouts: SSETimeouts{ReadHeader: 250 * time.Millisecond}}).newSSEServer()
	if srv.ReadHeaderTimeout != 250*time.Millisecond {
		t.Errorf("ReadHeaderTimeout = %v, want 250ms", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != DefaultReadTimeout || srv.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("zero fields should default, got Read=%v Idle=%v", srv.ReadTimeout, srv.IdleTimeout)
	}
}

// TestSSEServeRejectsStalledPartialRequest: a client that sends half a
// request line and then stalls is disconnected by ReadHeaderTimeout, on the
// server's initiative, instead of being held open forever.
func TestSSEServeRejectsStalledPartialRequest(t *testing.T) {
	const readHeader = 300 * time.Millisecond
	ln, shutdown := serveSSEOnPipe(t, New(Options{SSETimeouts: SSETimeouts{ReadHeader: readHeader}}))
	defer shutdown()

	conn := ln.dial(t)
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("GET /event")); err != nil {
		t.Fatalf("write partial request: %v", err)
	}

	start := time.Now()
	// Read until the server closes; the deadline is ~7x the header timeout,
	// so hitting it means the stalled connection was never dropped.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := io.ReadAll(conn)
	elapsed := time.Since(start)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("stalled partial request was never disconnected: no response after %v (ReadHeaderTimeout=%v)", elapsed, readHeader)
	}
	if err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read from stalled partial request: %v", err)
	}
	if elapsed < readHeader/3 {
		t.Fatalf("connection ended after %v, suspiciously before ReadHeaderTimeout (%v)", elapsed, readHeader)
	}
	if elapsed > readHeader+1500*time.Millisecond {
		t.Fatalf("stalled partial request dropped only after %v; want close near ReadHeaderTimeout (%v)", elapsed, readHeader)
	}
}

// TestSSEStreamSurvivesTimeoutsAndIsEndedByCancel: an /events stream stays
// alive (and keeps receiving events) long past every configured timeout, and
// ends promptly once the SSE base context is cancelled — exactly what Start
// does at shutdown.
func TestSSEStreamSurvivesTimeoutsAndIsEndedByCancel(t *testing.T) {
	const idle = 300 * time.Millisecond
	d := New(Options{SSETimeouts: SSETimeouts{ReadHeader: 100 * time.Millisecond, Read: idle, Idle: idle}})
	ln, shutdown := serveSSEOnPipe(t, d)
	defer shutdown()

	conn := ln.dial(t)
	defer func() { _ = conn.Close() }()

	// The event goes out after every configured timeout has expired: an idle
	// stream must outlive ReadHeader, Read and Idle and still deliver it.
	go func() {
		time.Sleep(2 * idle)
		d.bus.Emit("", "keepalive", nil)
	}()
	br := getStream(t, conn, "/events", 2*time.Second)
	expectData(t, conn, br, 2*time.Second)

	// Cancelling the base context must end the stream promptly, without the
	// client closing first.
	d.sseBaseCancel()
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second)) // generous: well under the 5s cap
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
func TestStartEndsOpenStreamsPromptlyOnCancel(t *testing.T) {
	requireBind(t)
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

	conn := dialSSE(t, d.sseAddr)
	defer func() { _ = conn.Close() }()

	// Confirm the stream is live before shutting down. ServeSSE writes its
	// headers with the first event, so emit before reading the response.
	go func() {
		time.Sleep(100 * time.Millisecond)
		d.bus.Emit("", "hello", nil)
	}()
	br := getStream(t, conn, "/events", 3*time.Second)
	expectData(t, conn, br, 3*time.Second)

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
	// The open stream ends on its own: the server closes it.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(br); err != nil {
		t.Fatalf("stream did not end after shutdown: %v", err)
	}
}

// dialSSE dials the daemon's SSE address, retrying until the server accepts
// (i.e. the daemon is up).
func dialSSE(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon SSE server never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// requireBind skips the test unless this environment can bind both a
// localhost TCP socket and a Unix socket. The Unix probe uses shortTempDir —
// the daemon's own control socket path convention — because a long path makes
// bind fail too (macOS sun_path is 104 bytes and a t.TempDir() dir named
// after a long test name exceeds it), and that must not be mistaken for a
// denied bind: it is what made this test skip on every normal host.
func requireBind(t *testing.T) {
	t.Helper()
	tl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("environment cannot bind tcp sockets: %v", err)
	}
	_ = tl.Close()
	ul, err := net.Listen("unix", filepath.Join(shortTempDir(t), "probe.sock"))
	if err != nil {
		t.Skipf("environment cannot bind unix sockets: %v", err)
	}
	_ = ul.Close()
}
