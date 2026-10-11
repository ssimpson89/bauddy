package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ssimpson89/bauddy/internal/mcpserver"
)

func newMCPCmd(version string) *cobra.Command {
	var (
		logDir     string
		noLog      bool
		allowRisky bool
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Let an AI agent use the console, as an MCP server (experimental)",
		Long: `bauddy mcp lets an AI agent such as Claude use a serial console through the
Model Context Protocol. Add it to your MCP client, e.g. for Claude Code:

  claude mcp add --scope user bauddy -- bauddy mcp

The agent can list ports, connect, run commands and read the output.
Commands are typed as fast as the device echoes them, and stop at the first
error or question. Reboots, erases, saving the configuration, debug, BREAK
and the like need your approval, which bauddy asks for through the client.
If the client can't ask, they're refused unless you start bauddy mcp with
--allow-risky.

Everything is logged, by default to ` + defaultLogDirHelp() + `.
Saved baud rates and names are used but never changed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := logDir
			switch {
			case noLog:
				dir = ""
			case dir == "":
				var err error
				if dir, err = defaultLogDir(); err != nil {
					return err
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err := mcpserver.Run(ctx, mcpserver.Options{Version: version, LogDir: dir, AllowRisky: allowRisky})
			if errors.Is(err, context.Canceled) {
				return nil // the client stopped the server
			}
			return err
		},
	}
	cmd.Flags().StringVar(&logDir, "log", "", "directory for the logs (default "+defaultLogDirHelp()+")")
	cmd.Flags().BoolVar(&noLog, "no-log", false, "don't log")
	cmd.Flags().BoolVar(&allowRisky, "allow-risky", false, "when the MCP client can't ask for your approval, send what needs it anyway")
	cmd.MarkFlagsMutuallyExclusive("log", "no-log")
	return cmd
}

// defaultLogDir is where bauddy mcp logs: the user's state directory
// ($XDG_STATE_HOME, else ~/.local/state), or %LocalAppData% on Windows.
func defaultLogDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "bauddy", "logs"), nil
	}
	if runtime.GOOS == "windows" {
		d, err := os.UserCacheDir() // %LocalAppData%
		return filepath.Join(d, "bauddy", "logs"), err
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "bauddy", "logs"), err
}

func defaultLogDirHelp() string {
	if runtime.GOOS == "windows" {
		return `%LocalAppData%\bauddy\logs`
	}
	return "~/.local/state/bauddy/logs"
}
