package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeResponder listens on sock and answers a 'status' message with the
// daemon's JSON envelope (ok + data carrying a pid), like a live daemon.
func fakeResponder(t *testing.T, sock string, pid int) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("fake responder listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var msg Message
				if err := json.NewDecoder(conn).Decode(&msg); err != nil {
					return
				}
				if msg.Type != "status" {
					return
				}
				data, _ := json.Marshal(map[string]interface{}{
					"pid":           pid,
					"project_count": 0,
				})
				_ = json.NewEncoder(conn).Encode(Response{OK: true, Data: data})
			}()
		}
	}()
}

// TestStartRefusesWhenDaemonAnswersStatus: a live daemon on the socket
// (fake responder) -> Start must refuse, name the daemon's pid, and leave
// the socket path untouched.
func TestStartRefusesWhenDaemonAnswersStatus(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "ctl.sock")
	const otherPID = 424242
	fakeResponder(t, sock, otherPID)

	d := New(Options{SocketPath: sock, PIDPath: filepath.Join(dir, "d.pid"), SSEAddr: "127.0.0.1:0"})
	err := d.Start(context.Background())
	if err == nil {
		t.Fatal("expected Start to refuse, got nil error")
	}
	if !strings.Contains(err.Error(), "daemon already running") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(otherPID)) {
		t.Fatalf("error should name the responding daemon's pid %d: %v", otherPID, err)
	}
	// The path must be untouched: the fake's socket still exists and accepts.
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket path was touched: %v", err)
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("existing daemon's socket no longer accepts: %v", err)
	}
	_ = c.Close()
	// We must not have written the pidfile either.
	if _, err := os.Stat(filepath.Join(dir, "d.pid")); !os.IsNotExist(err) {
		t.Fatalf("pidfile should not have been written, stat err = %v", err)
	}
}

// TestStartRefusesWhenPIDFilePointsAtLiveProcess: no live responder on the
// socket, but the pidfile names a live process -> refuse and tell the user
// to stop that pid; do not remove the stale-looking socket file.
func TestStartRefusesWhenPIDFilePointsAtLiveProcess(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "ctl.sock")
	pidf := filepath.Join(dir, "d.pid")
	// A stale socket file that is not a socket (dialing it fails).
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Pidfile naming a live process: this test process itself.
	if err := os.WriteFile(pidf, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}

	d := New(Options{SocketPath: sock, PIDPath: pidf, SSEAddr: "127.0.0.1:0"})
	err := d.Start(context.Background())
	if err == nil {
		t.Fatal("expected Start to refuse, got nil error")
	}
	if !strings.Contains(err.Error(), "daemon already running") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "stop") {
		t.Fatalf("error should tell the user to stop the pid first: %v", err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket file was removed although a live pid was recorded: %v", err)
	}
}

// TestStartRemovesStaleSocketAndRuns: no responder and no live recorded pid
// -> the socket file is stale; Start removes it, runs, answers status, and
// returns cleanly on ctx cancel.
func TestStartRemovesStaleSocketAndRuns(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "ctl.sock")
	pidf := filepath.Join(dir, "d.pid")
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := New(Options{SocketPath: sock, PIDPath: pidf, SSEAddr: "127.0.0.1:0"})
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()

	// Wait until the daemon answers status.
	var resp Response
	var lastErr error
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, lastErr = SendMessage(sock, Message{Type: "status"})
		if lastErr == nil && resp.OK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not come up on stale-removed socket: %v", lastErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var st struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(resp.Data, &st); err != nil || st.PID != os.Getpid() {
		t.Fatalf("status pid = %d (err %v), want %d", st.PID, err, os.Getpid())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}
