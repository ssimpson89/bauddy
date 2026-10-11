<h1 align="center"><img src="docs/logo.svg" alt="bauddy" width="420"></h1>

bauddy is a serial console for servers, switches and routers. Plug in a USB
console cable, run `bauddy`, and you're on the console. It works out the
port and baud rate for you, and the keys you need are always on screen.

It's also an MCP server: an AI agent such as Claude can connect to the
console, run commands and read the output, and bauddy asks you before it
sends anything that could reboot or wipe the device.

![bauddy connected to a switch](docs/screenshot.png)

## What it does

- **Finds the port.** With one USB adapter it connects straight away. With
  several, it asks which one.
- **Finds the baud rate.** It listens to the device and picks the rate that
  makes sense, then remembers it for that cable. It won't send anything to
  the device without asking first.
- **Shows the keys.** There's one key to remember, `ctrl+]`, and it's always
  on the bottom line. Press it to see the rest.
- **Tells you when something's wrong.** If the output turns to garbage, the
  bottom line suggests detecting the rate again. If the cable comes out, it
  turns red and reconnects when you plug it back in.
- **Keeps a log if you ask.** Press `ctrl+]` then `l`. The log includes what
  was already on screen, so you can turn it on after the fact.
- **Pastes carefully.** Pasting a config sends it a line at a time and waits
  for the device to catch up. It stops at the first error and asks before
  pasting anything that could reboot or wipe the device.
- **Lets an AI agent help (MCP).** `bauddy mcp` gives Claude, or any other
  Model Context Protocol client, the console: "the switch on my console
  cable isn't passing traffic, take a look". See
  [Use it with an AI agent](#use-it-with-an-ai-agent-mcp-server).

## Install

Download the file for your system from the
[releases page](https://github.com/ssimpson89/bauddy/releases), unzip it,
and put `bauddy` somewhere on your path.

Or, if you have Go:

```
go install github.com/ssimpson89/bauddy/cmd/bauddy@latest
```

## Use

```
bauddy                  # connect to your USB console cable
bauddy ttyUSB0          # a specific port
bauddy -b 9600          # a specific baud rate
bauddy list             # show the ports bauddy can see
bauddy keys             # show every key
```

Everything you type goes to the device, so Ctrl-C, Esc and the arrow keys work
as usual. When you want bauddy itself, press `ctrl+]` and the bottom line
shows what's next:

![the keys, after pressing ctrl+\]](docs/keys.png)

| Key | What it does |
|---|---|
| `?` | show every key |
| `b` | pick a baud rate |
| `d` | detect the baud rate |
| `r` | reconnect to the port |
| `l` | start or stop a log |
| `h` | scroll back through earlier output (the last 10,000 lines) |
| `/` | search earlier output |
| `c` | clear the screen |
| `B` | send BREAK (Cisco ROMMON, Linux SysRq) |
| `S` | tell a remote shell the window size |
| `q` | quit |
| `esc` | back to the console |

Lowercase keys act on bauddy. Capitals send something to the device.

## Use it with an AI agent (MCP server)

`bauddy mcp` is a Model Context Protocol (MCP) server for serial consoles. It
lets an AI agent such as Claude use the console: find the port, detect the
baud rate, run commands and read the output. It's new, so treat it as
experimental. Add it to Claude Code with:

```
claude mcp add --scope user bauddy -- bauddy mcp
```

Then ask something like "my switch on the console cable isn't passing
traffic, take a look". The agent can connect, run commands and read the
output. Commands go through the same careful paste, so they arrive intact
and stop at the first error or question.

- Some things need your approval, and bauddy asks you directly, not the
  agent:
  - rebooting
  - erasing
  - saving or committing the configuration
  - upgrades
  - `debug`
  - clearing BGP sessions or console lines
  - BREAK
  - answering the device's own `[confirm]` questions
  - anything typed at a bootloader prompt

  If your MCP client can't ask you, bauddy refuses, unless you start it with
  `bauddy mcp --allow-risky`.
- That check is a safety net, not a guarantee. It doesn't ask about
  configuration changes such as shutting an interface or changing a route,
  which can cut you off too, and aliases or scripts can get past it. Read
  what the agent wants to run before you allow it.
- Everything the device prints goes to the agent and into the log,
  including any passwords in the configuration. Logs go in
  `~/.local/state/bauddy/logs` (`%LocalAppData%\bauddy\logs` on Windows),
  readable only by you. Use `--log DIR` to choose another folder, or
  `--no-log` to turn logging off.
- The port can only be open in one program at a time. Close bauddy before
  the agent connects, and ask the agent to disconnect when you want it back.
- For Claude Desktop, add this to `claude_desktop_config.json`, with the full
  path to `bauddy`:

  ```json
  { "mcpServers": { "bauddy": { "command": "/usr/local/bin/bauddy", "args": ["mcp"] } } }
  ```

## Good to know

- **macOS** may block the downloaded file the first time, because it isn't
  signed by Apple. Allow it in System Settings → Privacy & Security, or run
  `xattr -d com.apple.quarantine bauddy`.
- **Linux** may say you don't have permission to open the port. bauddy tells
  you which group to join (usually `dialout`).
- **If `ctrl+]` is awkward on your keyboard**, use another key:
  `bauddy --escape ctrl+t`, or set `BAUDDY_ESCAPE=ctrl+t` in your shell.
- **Older equipment** that uses 7-bit framing (7E1) is detected too. You can
  also set it yourself with `-f 7E1`.
- **Logs** go in the current folder unless you say otherwise:
  `bauddy --log=~/console-logs` logs from the start into that folder.
  A log records what the device sends, including the commands it echoes
  back, so a password typed in a command (`enable secret …`) ends up in it.
- **Windows** builds are included but haven't been tested yet.
- **Arduino and ESP32 boards** may reset when bauddy connects. That's the
  board, not bauddy.

## License

MIT
