# Contributing

```
make build && ./bin/bauddy
make test        # go test -race ./...
make lint        # go vet ./...
make snapshot    # a local release build, see RELEASING.md
```

It needs Go 1.26 or later. Linux and Windows builds are pure Go. macOS needs
cgo, because the serial library lists ports through IOKit; Xcode's command
line tools provide it.

## Layout

```
cmd/bauddy/        entry point and version
internal/cli/       flags, subcommands, choosing the port and baud rate
internal/share/     sharing one console between windows and an AI tool
internal/mcpserver/ bauddy mcp: the MCP server an AI agent uses
internal/serialx/   the port: reading, reconnecting, baud and framing detection
internal/paste/     paced, echo-checked multi-line paste
internal/simdev/    a simulated switch, used by the paste tests
internal/termtext/  terminal output to plain text (logs, prompt detection)
internal/logfile/   session logs
internal/profile/   saved settings per adapter
internal/keymap/    command-mode keys; the status line and help are built from it
internal/tui/       the console, status line and overlays (Bubble Tea)
```

Paste pacing is tested against `internal/simdev`, which sends bytes at the
baud rate and drops input when its small buffer overflows, as real consoles
do. If you change how pastes are paced, add a case there.

## Ideas not done yet

- Line-ending and Backspace toggles (some gear wants CRLF or ^H)
- Hardware and XON/XOFF flow control
- Send a file / XMODEM, for firmware recovery
