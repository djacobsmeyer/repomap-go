package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/djacobsmeyer/repomap-go/internal/daemon"
	"github.com/djacobsmeyer/repomap-go/internal/mcp"
	"github.com/djacobsmeyer/repomap-go/internal/proxy"
	"github.com/djacobsmeyer/repomap-go/internal/standalone"
)

// Control socket and pidfile. Defaults are the historical fixed paths;
// --socket/--pid-file (or REPOMAP_SOCKET/REPOMAP_PID_FILE) point every
// subcommand at another daemon, e.g. a test daemon beside the user's.
var (
	socketPath = envOr("REPOMAP_SOCKET", daemon.DefaultSocketPath)
	pidPath    = envOr("REPOMAP_PID_FILE", daemon.DefaultPIDPath)
)

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func main() {
	root := &cobra.Command{
		Use:   "repomap",
		Short: "Persistent repo-map daemon + MCP proxy",
		PersistentPreRun: func(c *cobra.Command, args []string) {
			proxy.ControlSocket = socketPath
			proxy.ControlPIDFile = pidPath
		},
	}
	root.PersistentFlags().StringVar(&socketPath, "socket", socketPath, "daemon control socket (env REPOMAP_SOCKET)")
	root.PersistentFlags().StringVar(&pidPath, "pid-file", pidPath, "daemon pidfile (env REPOMAP_PID_FILE)")
	root.AddCommand(socketCmd())

	root.AddCommand(daemonCmd())
	root.AddCommand(addCmd())
	root.AddCommand(removeCmd())
	root.AddCommand(listCmd())
	root.AddCommand(mcpCmd())
	root.AddCommand(hintCmd())
	root.AddCommand(eventsCmd())
	root.AddCommand(doctorCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// --- daemon ------------------------------------------------------------------

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the background daemon",
	}

	var internal bool
	var sseAddr, projectSocketDir string
	var allowRoots []string
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background",
		RunE: func(c *cobra.Command, args []string) error {
			allowed := append(daemon.ParseRootList(os.Getenv("REPOMAP_ALLOWED_ROOTS")), allowRoots...)
			for i, a := range allowed {
				abs, err := filepath.Abs(a)
				if err != nil {
					return err
				}
				allowed[i] = abs
			}
			if internal {
				// Foreground execution (used by the spawned background process).
				d := daemon.New(daemon.Options{
					SocketPath:       socketPath,
					PIDPath:          pidPath,
					SSEAddr:          sseAddr,
					AllowedRoots:     allowed,
					ProjectSocketDir: projectSocketDir,
				})
				return d.Start(context.Background())
			}
			if pid, alive := daemon.IsRunning(pidPath); alive {
				fmt.Printf("daemon already running (pid %d)\n", pid)
				return nil
			}
			// Spawn ourselves with --internal.
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			childArgs := []string{"--socket", socketPath, "--pid-file", pidPath, "daemon", "start", "--internal"}
			if sseAddr != "" {
				childArgs = append(childArgs, "--sse-addr", sseAddr)
			}
			if projectSocketDir != "" {
				childArgs = append(childArgs, "--project-socket-dir", projectSocketDir)
			}
			for _, a := range allowed {
				childArgs = append(childArgs, "--allow-root", a)
			}
			cmd := exec.Command(exe, childArgs...)
			// The child gets the allowlist as flags only, so an inherited
			// REPOMAP_ALLOWED_ROOTS cannot widen or duplicate it.
			cmd.Env = withoutEnv(os.Environ(), "REPOMAP_ALLOWED_ROOTS")
			cmd.Stdout = nil
			cmd.Stderr = nil
			cmd.Stdin = nil
			cmd.SysProcAttr = detachAttr()
			if err := cmd.Start(); err != nil {
				return err
			}
			// Wait briefly to confirm startup.
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, alive := daemon.IsRunning(pidPath); alive {
					fmt.Printf("daemon started (pid %d)\n", cmd.Process.Pid)
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return fmt.Errorf("daemon did not come up within 3s")
		},
	}
	start.Flags().BoolVar(&internal, "internal", false, "internal foreground mode (used by spawn)")
	start.Flags().StringVar(&sseAddr, "sse-addr", "", "SSE listen address (default "+daemon.DefaultSSEAddr+")")
	start.Flags().StringArrayVar(&allowRoots, "allow-root", nil, "only register projects at or below this directory (repeatable; also REPOMAP_ALLOWED_ROOTS, colon-separated). Default: any directory")
	start.Flags().StringVar(&projectSocketDir, "project-socket-dir", "", "create per-project MCP sockets here (made 0700; must be private). Default: $TMPDIR")

	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		RunE: func(c *cobra.Command, args []string) error {
			if _, alive := daemon.IsRunning(pidPath); !alive {
				fmt.Println("daemon not running")
				return nil
			}
			resp, err := daemon.SendMessage(socketPath, daemon.Message{Type: "shutdown"})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			fmt.Println("daemon stopping")
			return nil
		},
	}

	var statusJSON bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(c *cobra.Command, args []string) error {
			pid, alive := daemon.IsRunning(pidPath)
			if !alive {
				fmt.Println("daemon: not running")
				return nil
			}
			resp, err := daemon.SendMessage(socketPath, daemon.Message{Type: "status"})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			var st map[string]interface{}
			_ = json.Unmarshal(resp.Data, &st)
			if statusJSON {
				b, _ := json.MarshalIndent(st, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			printStatus(pid, st)
			return nil
		},
	}
	status.Flags().BoolVar(&statusJSON, "json", false, "emit raw JSON")

	cmd.AddCommand(start, stop, status)
	return cmd
}

// --- add / remove / list -----------------------------------------------------

func addCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <path>",
		Short: "Register a project with the daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			abs, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"path": abs})
			resp, err := daemon.SendMessage(socketPath, daemon.Message{Type: "add", Payload: payload})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			fmt.Printf("added: %s\n", abs)
			return nil
		},
	}
}

func removeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <path>",
		Short: "Unregister a project from the daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			abs, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"path": abs})
			resp, err := daemon.SendMessage(socketPath, daemon.Message{Type: "remove", Payload: payload})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			fmt.Printf("removed: %s\n", abs)
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active projects",
		RunE: func(c *cobra.Command, args []string) error {
			resp, err := daemon.SendMessage(socketPath, daemon.Message{Type: "list"})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			var projects []map[string]interface{}
			_ = json.Unmarshal(resp.Data, &projects)
			if len(projects) == 0 {
				fmt.Println("(no projects registered)")
				return nil
			}
			printProjectTable(projects)
			return nil
		},
	}
}

// --- table rendering ---------------------------------------------------------

const daemonVersion = "v0.1.0"

func printStatus(pid int, st map[string]interface{}) {
	uptime := time.Duration(intFromAny(st["uptime_seconds"])) * time.Second
	projects, _ := st["projects"].([]interface{})
	fmt.Printf("repomap daemon %s  uptime: %s  (pid %d)\n", daemonVersion, formatDuration(uptime), pid)
	fmt.Printf("projects: %d active\n\n", len(projects))
	if len(projects) == 0 {
		return
	}
	rows := make([]map[string]interface{}, 0, len(projects))
	for _, p := range projects {
		if m, ok := p.(map[string]interface{}); ok {
			rows = append(rows, m)
		}
	}
	printProjectTable(rows)
}

func printProjectTable(projects []map[string]interface{}) {
	fmt.Printf("  %-30s  %6s  %-14s  %8s  %s\n", "PATH", "TAGS", "LAST REINDEX", "MEMORY", "STATE")
	for _, p := range projects {
		root, _ := p["root"].(string)
		tags := intFromAny(p["tags"])
		memBytes := intFromAny(p["memory_bytes"])
		lastReindex, _ := p["last_reindex"].(string)
		lastMCP, _ := p["last_mcp_call"].(string)

		idle := isProjectIdle(lastMCP)
		state := "active"
		prefix, suffix := "", ""
		if idle {
			state = "idle"
			prefix, suffix = ansiDim, ansiReset
		}

		fmt.Printf("%s  %-30s  %6d  %-14s  %8s  %s%s\n",
			prefix,
			truncLeft(homeShort(root), 30),
			tags,
			formatAgo(lastReindex),
			formatBytes(int64(memBytes)),
			state,
			suffix,
		)
	}
}

func isProjectIdle(lastMCP string) bool {
	if lastMCP == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, lastMCP)
	if err != nil || t.IsZero() {
		return false
	}
	return time.Since(t) > 30*time.Minute
}

func homeShort(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func truncLeft(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-(max-1):]
}

func formatAgo(rfc3339 string) string {
	if rfc3339 == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, rfc3339)
	if err != nil || t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func formatBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func intFromAny(v interface{}) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// --- doctor ------------------------------------------------------------------

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Print diagnostic info: binary path, GOPATH, daemon state, registered projects",
		RunE: func(c *cobra.Command, args []string) error {
			runDoctor()
			return nil
		},
	}
}

func runDoctor() {
	// binary path
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "(unknown)"
	}

	// GOPATH
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			gopath = filepath.Join(home, "go")
		} else {
			gopath = "(unknown)"
		}
	}

	// daemon
	pid, alive := daemon.IsRunning(pidPath)
	daemonState := "not running"
	if alive {
		daemonState = fmt.Sprintf("running (pid %d)", pid)
	}

	// socket
	socketState := socketPath + " (not found)"
	if _, statErr := os.Stat(socketPath); statErr == nil {
		socketState = socketPath + " (exists)"
	}

	fmt.Printf("binary:   %s\n", exe)
	fmt.Printf("GOPATH:   %s\n", gopath)
	fmt.Printf("daemon:   %s\n", daemonState)
	fmt.Printf("socket:   %s\n", socketState)

	if !alive {
		fmt.Println("projects: (daemon not running)")
		return
	}

	resp, err := daemon.SendMessage(socketPath, daemon.Message{Type: "list"})
	if err != nil || !resp.OK {
		fmt.Printf("projects: (could not query daemon: %v)\n", err)
		return
	}
	var projects []map[string]interface{}
	_ = json.Unmarshal(resp.Data, &projects)
	fmt.Printf("projects: %d registered\n", len(projects))
	for _, p := range projects {
		root, _ := p["root"].(string)
		lastMCP, _ := p["last_mcp_call"].(string)
		state := "active"
		if isProjectIdle(lastMCP) {
			state = "idle"
		}
		fmt.Printf("  %-40s  %s\n", root, state)
	}
}

// --- mcp / events ------------------------------------------------------------

func mcpCmd() *cobra.Command {
	var standaloneMode, agentMode bool
	var cacheDir, tools string
	cmd := &cobra.Command{
		Use:   "mcp <path>",
		Short: "STDIO MCP proxy: bridge stdin/stdout to the daemon's per-project socket",
		Long: "STDIO MCP proxy: bridge stdin/stdout to the daemon's per-project socket.\n\n" +
			"With --standalone, index <path> in this process and serve MCP on stdin/stdout\n" +
			"instead: no daemon, no sockets. Use this inside an OS sandbox (e.g. a Seatbelt\n" +
			"profile for a coding agent) so repomap is confined exactly like the agent.\n\n" +
			"--agent (with --standalone) serves the surface for small-context agents:\n" +
			"callers, outline and search, with short schemas and plain-text answers\n" +
			"inside a max_tokens budget. --tools picks the tools to expose (comma list;\n" +
			"agent mode also accepts the original five). `repomap hint` prints a usage\n" +
			"note to put in the agent's brief.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var toolList []string
			if tools != "" {
				toolList = strings.Split(tools, ",")
			}
			if standaloneMode {
				return standalone.Run(context.Background(), args[0], standalone.Options{
					CacheDir: cacheDir, Agent: agentMode, Tools: toolList,
				}, os.Stdin, os.Stdout)
			}
			if cacheDir != "" || agentMode || tools != "" {
				return fmt.Errorf("--cache-dir, --agent and --tools require --standalone")
			}
			return proxy.Run(args[0])
		},
	}
	cmd.Flags().BoolVar(&standaloneMode, "standalone", false, "index and serve in-process over stdio; never contact or start the daemon")
	cmd.Flags().StringVar(&cacheDir, "cache-dir", "", "with --standalone: tag cache directory (default <path>/.repomap)")
	cmd.Flags().BoolVar(&agentMode, "agent", false, "with --standalone: compact tools for small-context agents (callers, outline, search)")
	cmd.Flags().StringVar(&tools, "tools", "", "with --standalone: comma-separated tools to expose (default: all; with --agent: callers,outline,search)")
	return cmd
}

// hintCmd prints the agent-mode usage note (mcp.AgentHint), for a wrapper to
// put in an agent's brief so the tool descriptions can stay short.
func hintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hint",
		Short: "Print a short usage note for agents using `mcp --standalone --agent`",
		Args:  cobra.NoArgs,
		Run: func(c *cobra.Command, args []string) {
			fmt.Println(mcp.AgentHint)
		},
	}
}

// socketCmd prints a project's per-project MCP socket, registering the
// project if needed (mcp_connect). It never starts the daemon, so a caller
// can tell "daemon down" (exit 3) from other failures (exit 1).
func socketCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "socket <path>",
		Short: "Print the per-project MCP socket for <path> (registers it; never starts the daemon)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			abs, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"path": abs})
			resp, err := daemon.SendMessageTimeout(socketPath, daemon.Message{Type: "mcp_connect", Payload: payload}, timeout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "repomap: daemon not reachable at %s: %v\n", socketPath, err)
				os.Exit(3)
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			var data struct {
				Socket string `json:"socket"`
			}
			if err := json.Unmarshal(resp.Data, &data); err != nil || data.Socket == "" {
				return fmt.Errorf("daemon returned no socket path")
			}
			fmt.Println(data.Socket)
			return nil
		},
	}
	// mcp_connect indexes a new project synchronously before answering.
	cmd.Flags().DurationVar(&timeout, "timeout", 120*time.Second, "how long to wait for the daemon (first registration indexes the project)")
	return cmd
}

// withoutEnv returns env minus every NAME=... entry for name.
func withoutEnv(env []string, name string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}

func eventsCmd() *cobra.Command {
	var projectFilter string
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Tail the SSE event stream from the daemon",
		RunE: func(c *cobra.Command, args []string) error {
			return tailEvents(projectFilter)
		},
	}
	cmd.Flags().StringVar(&projectFilter, "project", "", "filter events to a specific project root")
	return cmd
}

// ANSI escape codes (no external dep).
const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiGray   = "\x1b[90m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

func tailEvents(projectFilter string) error {
	url := "http://127.0.0.1:7374/events"
	if projectFilter != "" {
		url += "?project=" + projectFilter
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect to daemon SSE (is it running?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon returned %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			fmt.Println(payload)
			continue
		}
		printEvent(ev)
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func printEvent(ev map[string]interface{}) {
	ts, _ := ev["ts"].(string)
	typ, _ := ev["type"].(string)
	project, _ := ev["project"].(string)

	// Trim timestamp to HH:MM:SS for readability.
	tsShort := ts
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		tsShort = t.Local().Format("15:04:05")
	}

	// Other fields.
	var rest []string
	for k, v := range ev {
		switch k {
		case "ts", "type", "project":
			continue
		}
		rest = append(rest, fmt.Sprintf("%s=%v", k, v))
	}

	fmt.Printf("%s%s%s  %s%-20s%s",
		ansiGray, tsShort, ansiReset,
		ansiCyan, typ, ansiReset,
	)
	if project != "" {
		fmt.Printf("  %s%s%s", ansiYellow, project, ansiReset)
	}
	if len(rest) > 0 {
		fmt.Printf("  %s", strings.Join(rest, " "))
	}
	fmt.Println()
}
