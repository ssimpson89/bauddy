package mcpserver

import (
	"cmp"
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ssimpson89/bauddy/internal/share"
)

// When the console is open in bauddy too, the user is watching, and the
// AI tool takes the keyboard for each command. The user can stop it at any
// time; bauddy then sends nothing more of it.

// sharedPort is a console the user may also have open in bauddy: a
// *share.Client.
type sharedPort interface {
	Lease(ctx context.Context) (context.Context, func() string, func(), error)
	Joined() bool
	Status() share.Status
}

func joined(p Port) bool {
	s, ok := p.(sharedPort)
	return ok && s.Joined()
}

// pausedByUser reports whether the user has paused the AI tool. It's
// checked before anything else, so a paused tool is told so, not about the
// state of the line it left.
func pausedByUser(p Port) bool {
	s, ok := p.(sharedPort)
	return ok && s.Status().Paused
}

// heardOnConsole reports whether the shared console has heard the device
// answer at baud, from anyone's typing: joining the user's window, the tool
// needn't press Enter to prove a rate the user is already working at.
func heardOnConsole(p Port, baud int) bool {
	s, ok := p.(sharedPort)
	return ok && s.Status().Heard == baud && baud != 0
}

// keyboard takes the keyboard for one tool call. The returned context ends
// if the user stops the tool; why then says how it was stopped. Call
// release when done.
func keyboard(ctx context.Context, c *console) (_ context.Context, why func() string, release func(), err error) {
	s, ok := c.port.(sharedPort)
	if !ok { // not shared: nobody else can type
		return ctx, func() string { return "" }, func() {}, nil
	}
	ctx, why, release, err = s.Lease(ctx)
	if err != nil {
		return nil, nil, nil, shareError(err)
	}
	return ctx, why, release, nil
}

// pausedText is what the AI tool is told while the user has it paused.
const pausedText = "Paused by the user in bauddy, so nothing was sent. Tell the user what you want to do; " +
	"they can let you type again in bauddy (its command key, usually ctrl+], then p). Don't retry until they do."

// shareError says what a refusal from a shared console means for the AI tool.
func shareError(err error) error {
	switch {
	case errors.Is(err, share.ErrPaused):
		return errors.New(pausedText)
	case errors.Is(err, share.ErrTyping):
		return errors.New("The user is typing in bauddy, so nothing was sent. Don't retry in a loop: " +
			"tell the user you're waiting, or ask them to finish.")
	case errors.Is(err, share.ErrBusy):
		return errors.New("The console is busy (baud detection), so nothing was sent. Try again in a moment.")
	}
	return err
}

// stoppedText tells the AI tool the user stopped it, and what that did.
func stoppedText(why string) string {
	what := map[string]string{
		share.StopCleared:     "Your unsent command was erased; Enter wasn't pressed.",
		share.StopLeft:        "Your typed text is still on the line; Enter wasn't pressed.",
		share.StopRunning:     "Your command was entered and may still be running.",
		share.StopInterrupted: "They pressed ctrl+c, so your command may have partly run.",
	}[why]
	if why == "disconnected" {
		return "The connection to the console dropped, so nothing more was sent. The line may be part-typed."
	}
	return strings.TrimSpace("The user stopped you in bauddy. "+what) +
		" You can't type until they let you (in bauddy: its command key, usually ctrl+], then p). " +
		"Don't retry; tell the user and wait."
}

// knownClients names MCP clients that don't send a title.
var knownClients = map[string]string{"claude-code": "Claude Code", "codex": "Codex", "claude-ai": "Claude"}

// clientName is the AI tool's name as its MCP client reports it, made safe
// for the user's status line.
func clientName(req *mcp.CallToolRequest) string {
	name := ""
	if req != nil {
		if info := req.ClientInfo(); info != nil {
			name = cmp.Or(info.Title, knownClients[info.Name], info.Name)
		}
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if r := []rune(strings.TrimSpace(name)); len(r) > 20 {
		name = string(r[:20])
	}
	return cmp.Or(strings.TrimSpace(name), "AI tool")
}
