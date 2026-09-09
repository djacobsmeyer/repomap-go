package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// freeTCPPort reserves a free localhost TCP port (best effort) for the SSE
// server in tests.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// TestShutdownBoundedWithActiveProjectAndSSEClient proves the shutdown
// guarantees documented in Start: with a live project (watcher active, a
// file change in flight) and an open SSE client connection, cancelling the
// daemon ctx makes Start return well within the documented bounds (5s SSE
// shutdown cap + one in-flight reindex batch). If any step of the shutdown
// could hang, this test would hit the 30s deadline.
func TestShutdownBoundedWithActiveProjectAndSSEClient(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "ctl.sock")
	pidf := filepath.Join(dir, "d.pid")
	sseAddr := fmt.Sprintf("127.0.0.1:%d", freeTCPPort(t))

	// A minimal parseable Go project.
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	mainGo := filepath.Join(proj, "main.go")
	src := []byte("package main\n\nfunc Main() int {\n\treturn helper(1)\n}\n\nfunc helper(x int) int {\n\treturn x + 1\n}\n")
	if err := os.WriteFile(mainGo, src, 0o644); err != nil {
		t.Fatal(err)
	}

	d := New(Options{SocketPath: sock, PIDPath: pidf, SSEAddr: sseAddr})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()

	// Wait for the daemon to answer status.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := SendMessage(sock, Message{Type: "status"})
		if err == nil && resp.OK {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("daemon did not come up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Register the project (initial index + watcher + MCP listener).
	payload, _ := json.Marshal(map[string]string{"path": proj})
	deadline = time.Now().Add(10 * time.Second)
	for {
		resp, err := SendMessage(sock, Message{Type: "add", Payload: payload})
		if err == nil && resp.OK {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("add project failed: %v (%s)", err, resp.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Open a raw TCP connection to the SSE endpoint so a handler goroutine
	// is active inside its event select at shutdown time. (An HTTP client's
	// Do would block until the first event, because ServeSSE only writes
	// headers on the first event; the raw connection avoids that entirely
	// and still exercises the "active SSE handler" case that
	// sseServer.Shutdown must wait out.)
	sseConn, err := net.Dial("tcp", sseAddr)
	if err != nil {
		cancel()
		t.Fatalf("sse dial: %v", err)
	}
	defer func() { _ = sseConn.Close() }()
	fmt.Fprintf(sseConn, "GET /events HTTP/1.1\r\nHost: x\r\nAccept: text/event-stream\r\n\r\n")
	// Let the handler reach its select.
	time.Sleep(200 * time.Millisecond)

	// Generate watcher activity so shutdown races an in-flight batch.
	if err := os.WriteFile(mainGo, append(src, []byte("\nfunc extra() {}\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	// Now cancel and assert bounded shutdown.
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error on shutdown: %v", err)
		}
		if el := time.Since(start); el > 15*time.Second {
			t.Fatalf("shutdown took %v; expected well under the 15s bound", el)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown hung: Start did not return 30s after ctx cancel")
	}
}
