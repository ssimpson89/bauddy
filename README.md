<h1 align="center"><img src="docs/logo.svg" alt="bauddy" width="420"></h1>

bauddy is a serial console for servers, switches and routers. Plug in a USB
console cable, run `bauddy`, and you're on the console. It works out the
port and baud rate for you, and the keys you need are always on screen.

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

![the keys, after pressing ctrl+]](docs/keys.png)

| Key | What it does |
|---|---|
| `?` | show every key |
| `b` | pick a baud rate |
| `d` | detect the baud rate |
| `l` | start or stop a log |
| `q` | quit |
| `B` | send BREAK (Cisco ROMMON, Linux SysRq) |

Lowercase keys act on bauddy. Capitals send something to the device.

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
- **Arduino and ESP32 boards** may reset when bauddy connects. That's the
  board, not bauddy.

## License

MIT
