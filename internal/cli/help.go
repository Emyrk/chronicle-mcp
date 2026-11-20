package cli

import (
	"fmt"
	"io"
	"strings"
)

const usage = `Usage: chronicle-mcp <command> [flags]

Commands:
  serve         Run the shared daemon in the foreground
  start         Start in the background or reuse a compatible daemon
  status        Show browser ownership, connections, and idle state
  stop          Gracefully stop the shared daemon
  auth headers  Emit private authentication headers for an MCP client
  help          Show help, optionally for a command

Use "chronicle-mcp help <command>" for command flags.
`

func printHelp(out io.Writer, command string) error {
	if command == "" {
		_, err := io.WriteString(out, usage)
		return err
	}
	descriptions := map[string]string{
		"serve":        "Run the shared daemon in the foreground.",
		"start":        "Start in the background or reuse a compatible daemon. Never replace an incompatible service automatically.",
		"status":       "Show browser ownership, connections, and idle state without exposing credentials.",
		"stop":         "Gracefully stop the shared daemon. This disconnects all clients, not just this session.",
		"auth":         "Emit private authentication headers for an MCP client's header helper. Do not paste the output into chat or commit it.",
		"auth headers": "Emit private authentication headers for an MCP client's header helper. Do not paste the output into chat or commit it.",
	}
	description, ok := descriptions[command]
	if !ok {
		return fmt.Errorf("unknown help topic %q", command)
	}
	if command == "auth" {
		command = "auth headers"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: chronicle-mcp %s [flags]\n\n%s\n\nFlags:\n", command, description)
	b.WriteString("  --state-dir PATH   Private state directory (default: OS user config directory/chronicle-mcp)\n")
	if command == "serve" || command == "start" {
		b.WriteString(`  --port PORT        Loopback listener port (default: 17643; 0 selects an ephemeral port)
  --origin ORIGIN    Additional exact browser origin (default: https://chronicleclassic.com)
  --idle-timeout D   Inactivity timeout (default: 6h; 0 disables)
  --agent-lease D    Agent inactivity lease (default: 5m; positive, at most 24h)
`)
	}
	if command == "stop" {
		b.WriteString("  --force            Explicitly disconnect active agents and cancel pending calls\n")
	}
	b.WriteString("  -h, --help         Show this help\n")
	_, err := io.WriteString(out, b.String())
	return err
}

// Handle help before state-path resolution or any daemon/credential access.
func helpRequest(args []string) (bool, string) {
	if len(args) == 0 {
		return true, ""
	}
	if args[0] == "help" {
		return true, strings.Join(args[1:], " ")
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return true, ""
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		return true, args[0]
	}
	if len(args) == 3 && args[0] == "auth" && args[1] == "headers" && (args[2] == "--help" || args[2] == "-h") {
		return true, "auth headers"
	}
	return false, ""
}
