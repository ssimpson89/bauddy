// Package cli wires flags and subcommands to the TUI.
package cli

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/ssimpson89/bauddy/internal/keymap"
	"github.com/ssimpson89/bauddy/internal/profile"
	"github.com/ssimpson89/bauddy/internal/serialx"
	"github.com/ssimpson89/bauddy/internal/share"
	"github.com/ssimpson89/bauddy/internal/tui"
)

const defaultBaud = 115200

// noPortsHint adds how to name a port yourself, with an example in this
// system's style, for a port bauddy can't see (a pseudo-terminal, say).
func noPortsHint() string {
	return serialx.ErrNoPorts.Error() + " You can also name the port: bauddy " + portExample()
}

func portExample() string {
	return cmp.Or(map[string]string{"darwin": "/dev/cu.usbserial-1410", "windows": "COM3"}[runtime.GOOS], "/dev/ttyUSB0")
}

// isTerminal says whether f is a terminal, not a pipe, a file or a device
// such as /dev/null.
func isTerminal(f *os.File) bool { return term.IsTerminal(f.Fd()) }

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

With no port given, it uses your serial cable, asks which one if there are
several, or waits for one to be plugged in. A port can be a path or a short
name like ttyUSB0.

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

	cmd.AddCommand(newListCmd(), newKeysCmd(o), newMCPCmd(version))
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
	port, waited, err := resolvePort(arg, store)
	if errors.Is(err, tui.ErrQuit) {
		return nil
	}
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

	// A bauddy started from Claude Code's shell is the AI tool looking, so
	// it joins as one: no earlier output, and it can't type or unpause.
	byAgent := os.Getenv("CLAUDECODE") != ""
	opts := share.Options{Adapter: port.Adapter(), Path: port.OpenPath(), Baud: baud, Framing: framing,
		Replay: !byAgent, Agent: byAgent, Name: map[bool]string{true: "Claude Code"}[byAgent]}
	sess, err := share.Open(opts)
	if waited {
		// A cable that has just been plugged in can be refused for a moment
		// on Linux, until udev gives the device its group: retry for up to 2 s.
		for i := 0; serialx.IsPermissionDenied(err) && i < 10; i++ {
			time.Sleep(200 * time.Millisecond)
			sess, err = share.Open(opts)
		}
	}
	if err != nil {
		return serialx.OpenError(port.OpenPath(), err)
	}
	defer sess.Close()
	var ignoredBaud string // said after the TUI, where it can be read
	if sess.Joined() {
		source = "joined" // another bauddy has the console: use its rate, don't detect
		if o.baud > 0 && o.baud != sess.Baud() {
			ignoredBaud = fmt.Sprintf("--baud %d was ignored: the console was already open at %d baud in another bauddy", o.baud, sess.Baud())
		}
	}
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
		Save:       save,
		BackspaceH: saved.BackspaceH, AddCR: saved.AddCR,
		SaveKeys: func(bs, cr bool) error { return store.RememberKeys(port.Key, bs, cr) },
	})
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m).Run()
	for _, path := range m.Finish() {
		fmt.Fprintln(os.Stderr, "log saved:", path)
	}
	if ignoredBaud != "" {
		fmt.Fprintln(os.Stderr, ignoredBaud)
	}
	if err := sess.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	if s := sess.Status(); s.Agent != "" && s.Windows <= 1 {
		fmt.Fprintln(os.Stderr, s.Agent, "still has this console open. Run bauddy again to rejoin.")
	}
	return err
}

// escapeKey is the key that opens command mode: the flag, else
// BAUDDY_ESCAPE, else ctrl+].
func escapeKey(flag string) string {
	return cmp.Or(flag, os.Getenv("BAUDDY_ESCAPE"), keymap.DefaultEscapeKey)
}

// resolvePort turns an optional argument into a port. With no argument, in a
// terminal, a lone port connects straight away; otherwise the cable screen
// waits for one or asks which, built-in ports included. Then waited is true:
// the cable may have only just been plugged in, so it may still be getting
// its permissions. Without a terminal, several ports is an error that lists
// them (bauddy mcp and scripts keep Choose's rule: a lone USB cable wins).
func resolvePort(arg string, store *profile.Store) (serialx.PortInfo, bool, error) {
	ports, err := serialx.List()
	if err != nil {
		return serialx.PortInfo{}, false, err
	}
	if askForCable(arg, ports, isTerminal(os.Stdin) && isTerminal(os.Stdout)) {
		hint := "Plug in your console cable and bauddy will connect to it.\n\n" +
			"Already plugged in? The cable may need a driver from its maker.\n\n" +
			"Know the port? Press q, then run: bauddy " + portExample()
		p, err := tui.PickCable(ports, hint, func(p serialx.PortInfo) string { return remembered(store.Adapter(p.Key)) },
			func(p serialx.PortInfo) bool { return share.InUse(p.Adapter(), p.OpenPath()) })
		return p, true, err
	}
	p, err := serialx.Choose(arg, ports, store.KeyForName)
	var several *serialx.SeveralError
	switch {
	case errors.As(err, &several):
		var list strings.Builder
		for i, p := range several.Ports {
			fmt.Fprintf(&list, "  %d) %s  %s\n", i+1, p.OpenPath(), describe(p))
		}
		return p, false, fmt.Errorf("several serial ports found; pass one as an argument:\n%s", list.String())
	case errors.Is(err, serialx.ErrNoPorts):
		return p, false, errors.New(noPortsHint())
	case errors.Is(err, serialx.ErrUnplugged):
		return p, false, fmt.Errorf("%w; 'bauddy list' shows what is", err)
	}
	return p, false, err
}

// askForCable says whether the cable screen picks the port: in a terminal,
// with no port named, unless exactly one port is there. This deliberately
// differs from Choose, where a lone USB cable beats built-in ports: a
// person at a keyboard is asked, with the USB cable first under the cursor,
// since a server's real ttyS0 may be the console they want. (Ports that
// aren't real cables, like Intel AMT's, never get this far: List leaves
// them out.) Scripts and bauddy mcp can't answer, so they keep Choose's rule.
func askForCable(arg string, ports []serialx.PortInfo, terminal bool) bool {
	return terminal && arg == "" && len(ports) != 1
}

// remembered is what bauddy knows about a cable, for the cable screen:
// "core-sw · 9600 last time".
func remembered(a profile.Adapter) string {
	var parts []string
	if a.Name != "" {
		parts = append(parts, a.Name)
	}
	if a.Baud > 0 {
		parts = append(parts, strconv.Itoa(a.Baud)+" last time")
	}
	return strings.Join(parts, " · ")
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
