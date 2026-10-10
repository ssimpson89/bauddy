// Package cli wires flags and subcommands to the TUI.
package cli

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"go.bug.st/serial"

	"github.com/ssimpson89/bauddy/internal/keymap"
	"github.com/ssimpson89/bauddy/internal/profile"
	"github.com/ssimpson89/bauddy/internal/serialx"
	"github.com/ssimpson89/bauddy/internal/tui"
)

const defaultBaud = 115200

const noPorts = "no serial ports found; is the adapter plugged in?"

// noPortsHint adds how to name a port yourself, with an example in this
// system's style, for a port bauddy can't see (a pseudo-terminal, say).
func noPortsHint() string {
	example := map[string]string{"darwin": "/dev/cu.usbserial-1410", "windows": "COM3"}[runtime.GOOS]
	return noPorts + " You can also name the port: bauddy " + cmp.Or(example, "/dev/ttyUSB0")
}

type rootOpts struct {
	baud    int
	framing string
	escape  string
	log     string
	name    string
}

// NewRootCmd builds the `bauddy` command tree.
func NewRootCmd(version string) *cobra.Command {
	o := &rootOpts{}
	cmd := &cobra.Command{
		Use:   "bauddy [port]",
		Short: "A serial console with one key to remember",
		Long: `bauddy connects to a serial console (servers, switches, routers, boards).

With no port given, it uses your USB serial adapter, or asks which one if
there are several. A port can be a path or a short name like ttyUSB0.

With no --baud, it uses the last rate used with this adapter. For a new
adapter it listens for output and detects the rate, and asks before sending
anything.

Inside, press ctrl+] then a key. 'bauddy keys' lists them all. If ctrl+] is
awkward on your keyboard, use --escape ctrl+t (or set BAUDDY_ESCAPE).`,
		Args:              cobra.MaximumNArgs(1),
		Version:           version,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{HiddenDefaultCmd: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			port := ""
			if len(args) == 1 {
				port = args[0]
			}
			return run(port, o, version)
		},
	}
	cmd.Flags().IntVarP(&o.baud, "baud", "b", 0, "baud rate; skips detection and is saved for this adapter")
	cmd.Flags().StringVarP(&o.framing, "framing", "f", "", "data bits, parity, stop bits, e.g. 7E1 (default: saved, detected or 8N1)")
	cmd.PersistentFlags().StringVar(&o.escape, "escape", "", "key that opens the bauddy keys instead of ctrl+], e.g. ctrl+t (or set BAUDDY_ESCAPE)")
	cmd.Flags().StringVar(&o.log, "log", "", "log from the start: --log (current directory), --log=DIR or --log=FILE.log")
	cmd.Flags().Lookup("log").NoOptDefVal = "."
	cmd.Flags().StringVar(&o.name, "name", "", "save a name for this adapter (see README)")
	_ = cmd.Flags().MarkHidden("name")

	cmd.AddCommand(newListCmd(), newKeysCmd(o))
	return cmd
}

func run(arg string, o *rootOpts, version string) error {
	if o.baud < 0 {
		return fmt.Errorf("--baud must be a positive number, got %d", o.baud)
	}
	esc := escapeKey(o.escape)
	if err := keymap.CheckEscape(esc); err != nil {
		return err
	}
	if fi, err := os.Stat(arg); err == nil && fi.IsDir() {
		return fmt.Errorf("%s is a directory, not a port; to log there, use --log=%s", arg, arg)
	}
	store, err := profile.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: can't read saved settings:", err)
	}
	port, err := resolvePort(arg, store)
	if err != nil {
		return err
	}
	saved := store.Adapter(port.Key)

	// Baud: --baud, else saved for this adapter, else detected, else 115200.
	baud, source := defaultBaud, "default"
	switch {
	case o.baud > 0:
		baud, source = o.baud, "flag"
	case saved.Baud > 0:
		baud, source = saved.Baud, "saved"
	}
	// Framing: --framing, else saved, else 8N1 (detection may still find 7-bit).
	framing := serialx.Framing8N1
	if o.framing != "" {
		if framing, err = serialx.ParseFraming(o.framing); err != nil {
			return err
		}
	} else if f, err := serialx.ParseFraming(saved.Framing); err == nil {
		framing = f
	}

	sess, err := serialx.Open(port.OpenPath(), baud, framing)
	if err != nil {
		return friendlyOpenError(port.OpenPath(), err)
	}
	defer sess.Close()
	save := func(baud int, f serialx.Framing) error { return store.RememberLine(port.Key, baud, f.String()) }
	// Save what the user chose explicitly; detected rates are saved by the
	// TUI once they've worked.
	if source == "flag" || (o.framing != "" && source == "saved") {
		if err := save(baud, framing); err != nil {
			fmt.Fprintln(os.Stderr, "warning: can't save the baud rate:", err)
		}
	}
	name := cmp.Or(saved.Name, filepath.Base(port.Path))
	if o.name != "" {
		if err := saveName(store, port.Key, o.name); err != nil {
			fmt.Fprintln(os.Stderr, "warning:", err)
		} else {
			name = o.name
		}
	}

	m, err := tui.New(tui.Options{
		Session: sess, Name: name, Keymap: keymap.New(esc), LogPath: o.log, Version: version,
		Detect: source == "default", Recheck: source == "saved", KeepFraming: o.framing != "",
		Save: save,
	})
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m).Run()
	for _, path := range m.Finish() {
		fmt.Fprintln(os.Stderr, "log saved:", path)
	}
	return err
}

// escapeKey is the key that opens command mode: the flag, else
// BAUDDY_ESCAPE, else ctrl+].
func escapeKey(flag string) string {
	return cmp.Or(flag, os.Getenv("BAUDDY_ESCAPE"), keymap.DefaultEscapeKey)
}

// resolvePort turns an optional argument into a port. With no argument, a
// lone USB adapter connects straight away; otherwise the user picks one.
func resolvePort(arg string, store *profile.Store) (serialx.PortInfo, error) {
	ports, err := serialx.List()
	if err != nil {
		return serialx.PortInfo{}, err
	}
	if arg != "" {
		if p, ok := serialx.Find(ports, arg); ok {
			return p, nil
		}
		if key, ok := store.KeyForName(arg); ok {
			for _, p := range ports {
				if p.Key == key {
					return p, nil
				}
			}
			if strings.HasPrefix(key, "/") {
				return serialx.Manual(key), nil // saved for a path that isn't enumerated
			}
			return serialx.PortInfo{}, fmt.Errorf("%s's adapter isn't plugged in; 'bauddy list' shows what is", arg)
		}
		return serialx.Manual(arg), nil // not enumerated (e.g. a pty); try it anyway
	}

	var usb []serialx.PortInfo
	for _, p := range ports {
		if p.USB {
			usb = append(usb, p)
		}
	}
	switch {
	case len(usb) == 1:
		return usb[0], nil
	case len(ports) == 0:
		return serialx.PortInfo{}, errors.New(noPortsHint())
	case len(ports) == 1:
		return ports[0], nil
	}
	return choosePort(ports)
}

// choosePort asks which of several ports to use.
func choosePort(ports []serialx.PortInfo) (serialx.PortInfo, error) {
	var list strings.Builder
	for i, p := range ports {
		fmt.Fprintf(&list, "  %d) %s  %s\n", i+1, p.OpenPath(), describe(p))
	}
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return serialx.PortInfo{}, fmt.Errorf("several serial ports found; pass one as an argument:\n%s", list.String())
	}
	fmt.Fprint(os.Stderr, "Several serial ports found:\n"+list.String()+"Connect to which? [1]: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return ports[0], nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(ports) {
		return serialx.PortInfo{}, fmt.Errorf("not a choice: %q", line)
	}
	return ports[n-1], nil
}

// saveName labels the adapter at key, refusing names that are also port
// names, since `bauddy NAME` would open the port instead.
func saveName(store *profile.Store, key, name string) error {
	ports, _ := serialx.List()
	if _, ok := serialx.Find(ports, name); ok {
		return fmt.Errorf("not saving the name %q: it's also the name of a port", name)
	}
	if err := store.RememberName(key, name); err != nil {
		return fmt.Errorf("can't save the name: %w", err)
	}
	return nil
}

func describe(p serialx.PortInfo) string {
	return strings.TrimSpace(p.USBID() + " " + p.Product)
}

// friendlyOpenError explains the common reasons a port won't open, with
// what to do about them.
func friendlyOpenError(path string, err error) error {
	var pe *serial.PortError
	if errors.As(err, &pe) {
		switch pe.Code() {
		case serial.PortBusy:
			return fmt.Errorf("%s is in use by another program (screen, minicom, another bauddy); close it and try again", path)
		case serial.PermissionDenied:
			return fmt.Errorf("no permission to open %s; on Linux, add yourself to the group that owns it "+
				"(usually dialout, uucp on Arch): sudo usermod -aG dialout $USER, then log out and back in", path)
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s doesn't exist; 'bauddy list' shows the ports", path)
	}
	return fmt.Errorf("can't open %s: %w", path, err)
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List serial ports with saved names and settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			ports, err := serialx.List()
			if err != nil {
				return err
			}
			if len(ports) == 0 {
				fmt.Println(noPortsHint())
				return nil
			}
			store, err := profile.Load()
			if err != nil {
				fmt.Fprintln(os.Stderr, "warning: can't read saved settings:", err)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PORT\tNAME\tUSB ID\tSERIAL\tPRODUCT\tSAVED")
			for _, p := range ports {
				a := store.Adapter(p.Key)
				line := "-"
				if a.Baud > 0 {
					line = strings.TrimSpace(strconv.Itoa(a.Baud) + " " + a.Framing)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.OpenPath(), cmp.Or(a.Name, "-"),
					cmp.Or(p.USBID(), "-"), cmp.Or(p.Serial, "-"), cmp.Or(p.Product, "-"), line)
			}
			return w.Flush()
		},
	}
}

func newKeysCmd(o *rootOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "keys",
		Short: "Print every key",
		RunE: func(cmd *cobra.Command, args []string) error {
			esc := escapeKey(o.escape)
			if err := keymap.CheckEscape(esc); err != nil {
				return err
			}
			fmt.Print(keymap.New(esc).Cheatsheet())
			return nil
		},
	}
}
