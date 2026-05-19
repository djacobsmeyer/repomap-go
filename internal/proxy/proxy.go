// Package proxy implements the STDIO MCP proxy: it bridges the local MCP
// client (talking over stdin/stdout) to the daemon's per-project Unix socket.
package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/yourusername/repomap-go/internal/daemon"
)

// Run is the entry point. It blocks until either end closes.
func Run(projectRoot string) error {
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return fmt.Errorf("abs path: %w", err)
	}

	socketPath, err := connectToProject(abs)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}

	// Wait briefly for the per-project socket file to appear.
	if err := waitForSocket(socketPath, 5*time.Second); err != nil {
		return fmt.Errorf("wait for project socket: %w", err)
	}

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("dial project socket: %w", err)
	}
	defer conn.Close()

	// Bidirectional pipe.
	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, os.Stdin)
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(os.Stdout, conn)
		errCh <- err
	}()

	// First end to finish (EOF or error) wins. The other side will unblock
	// when the connection closes.
	<-errCh
	return nil
}

// connectToProject asks the daemon for the per-project socket, starting the
// daemon if it isn't running.
func connectToProject(abs string) (string, error) {
	socket, err := sendMCPConnect(abs)
	if err == nil {
		return socket, nil
	}

	// Try to start the daemon and retry once.
	if startErr := startDaemon(); startErr != nil {
		return "", fmt.Errorf("daemon not running and could not start: %v (original: %v)", startErr, err)
	}
	// Give the daemon a moment to come up.
	time.Sleep(500 * time.Millisecond)

	// Retry a few times in case startup is still finishing.
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error = err
	for time.Now().Before(deadline) {
		socket, err = sendMCPConnect(abs)
		if err == nil {
			return socket, nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return "", lastErr
}

func sendMCPConnect(abs string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"path": abs})
	resp, err := daemon.SendMessage(daemon.DefaultSocketPath, daemon.Message{
		Type:    "mcp_connect",
		Payload: payload,
	})
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("%s", resp.Error)
	}
	var data struct {
		Socket     string `json:"socket"`
		SocketPath string `json:"socket_path"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return "", err
	}
	if data.Socket != "" {
		return data.Socket, nil
	}
	if data.SocketPath != "" {
		return data.SocketPath, nil
	}
	return "", fmt.Errorf("daemon returned no socket path")
}

func startDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "daemon", "start")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	return cmd.Start()
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("socket did not appear: %s", path)
}
