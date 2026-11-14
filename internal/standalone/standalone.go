// Package standalone runs one project's index and MCP server in-process,
// speaking JSON-RPC on stdin/stdout. It binds no socket and never contacts
// the daemon, so it can run inside the same OS sandbox as the agent that
// uses it: every file it reads, every cache write and every git command it
// runs is then confined exactly like the agent's own tools.
package standalone

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/djacobsmeyer/repomap-go/internal/mcp"
	"github.com/djacobsmeyer/repomap-go/internal/project"
)

// Options configure Run.
type Options struct {
	// CacheDir is the tag-cache directory (empty = <root>/.repomap).
	CacheDir string
	// Agent and Tools select the tool surface; see mcp.Options.
	Agent bool
	Tools []string
}

// Run indexes root, then serves MCP on in/out until in reaches EOF. The
// initial index completes before the first request is read, so the first
// answer is never served from a half-built index.
func Run(ctx context.Context, root string, opts Options, in io.Reader, out io.Writer) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("abs path: %w", err)
	}
	if fi, err := os.Stat(abs); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("not a directory: %s", abs)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p, err := project.New(abs, nil)
	if err != nil {
		return err
	}
	// Build the server first so a bad --tools fails before indexing.
	srv, err := mcp.NewWithOptions(abs, p, mcp.Options{Agent: opts.Agent, Tools: opts.Tools})
	if err != nil {
		return err
	}
	p.DisableSocket()
	if opts.CacheDir != "" {
		cd, err := filepath.Abs(opts.CacheDir)
		if err != nil {
			return err
		}
		p.SetCacheDir(cd)
	}
	if err := p.Start(ctx); err != nil {
		return err
	}
	defer p.Stop()

	srv.ServeStream(in, out)
	return nil
}
