package mcpserver

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ssimpson89/bauddy/internal/paste"
)

// Why something needs the user's approval, completing "It ...".
const (
	whyRestart = "can restart the device, or erase or replace its configuration or software"
	whySave    = "saves or commits the configuration, so changes survive a restart"
	whyDebug   = "can flood the console and slow the device down"
	whyClear   = "can drop routing sessions or connections, or clear the logs"
	whyBoot    = "is typed at a bootloader prompt, where commands can erase or replace the software"
	whyAnswer  = "answers the device's question"
	whyProbe   = "presses Enter on the device: at a boot menu it starts the default entry, and at a [confirm] it confirms"
	whyRecall  = "runs a line recalled from history or completed with tab, which bauddy can't read for certain"
	whyBreak   = "halts Sun and Solaris systems and stops Cisco gear booting; on Linux the next key typed is a " +
		"SysRq command, and b reboots at once"
)

var (
	// These match commands, as paste.Commands returns them, that an agent
	// needs approval for on top of paste.Dangerous. A person pasting into
	// the console isn't asked about them.
	saves  = regexp.MustCompile(`^(wr\w*( mem\w*)?|copy r\w* (nvram:)?s(t\S*)?|commit( .*)?|save( .*)?)$`)
	debugs = regexp.MustCompile(`^deb(ug?)?( |$)`)
	clears = regexp.MustCompile(`^(cl|cle|clea|clear) (ip |ipv6 )?(bgp|ospf|eigrp|route|crypto|line|logging|interface|nat)\b`)

	// bootloader matches a bootloader prompt: Cisco ROMMON and switch:,
	// loader>, U-Boot, GRUB.
	bootloader = regexp.MustCompile(`(?i)^(rommon ?\d* ?>|switch:|loader>|=>|u-boot>|grub>) ?$`)
	// promptLike matches a cursor line that looks like a prompt with nothing
	// typed after it.
	promptLike = regexp.MustCompile(`[#>$%] ?$`)
	// confirming matches the cursor line when the device wants a yes or no
	// before doing something.
	confirming = regexp.MustCompile(`(?i)\[confirm\]|\[y(es)?/n(o)?\]|\(y(es)?/n(o)?\)|\[yes,\s*no\]|` +
		`are you sure|proceed\s*\?|continue\s*\?`)
	// promptEnd finds where a prompt may end and typing begin.
	promptEnd = regexp.MustCompile(`[#>$%]`)
)

// risk says why line needs approval, or "" if it doesn't.
func risk(line string) string {
	for _, cmd := range paste.Commands(line) {
		switch {
		case saves.MatchString(cmd) && !strings.HasPrefix(cmd, "commit check"):
			return whySave
		case paste.Dangerous(cmd):
			return whyRestart
		case debugs.MatchString(cmd):
			return whyDebug
		case clears.MatchString(cmd):
			return whyClear
		}
	}
	return ""
}

// lineRisk is risk for the device's cursor line, prompt and all. The prompt
// can't be told apart from what was typed for certain, so every place a
// prompt could end is tried.
func lineRisk(line string) string {
	if why := risk(line); why != "" {
		return why
	}
	for _, loc := range promptEnd.FindAllStringIndex(line, -1) {
		if why := risk(line[loc[1]:]); why != "" {
			return why
		}
	}
	return ""
}

// looksLikePrompt reports whether the cursor line looks like a prompt with
// nothing typed after it.
func looksLikePrompt(cursor string) bool {
	return promptLike.MatchString(cursor) || bootloader.MatchString(cursor)
}

// typed returns what's typed after the prompt on the cursor line: after the
// last prompt seen if the line starts with it, else after the line's last
// # > $ or %.
func (l line) typed() string {
	if l.prompt != "" && strings.HasPrefix(l.cursor, l.prompt) {
		return strings.TrimSpace(l.cursor[len(l.prompt):])
	}
	if i := strings.LastIndexAny(l.cursor, "#>$%"); i >= 0 {
		return strings.TrimSpace(l.cursor[i+1:])
	}
	return ""
}

// atBootloader reports whether the cursor is at a bootloader prompt, with
// or without something typed after it.
func (l line) atBootloader() bool {
	if bootloader.MatchString(l.cursor) {
		return true
	}
	return l.prompt != "" && strings.HasPrefix(l.cursor, l.prompt) && bootloader.MatchString(l.prompt)
}

// confirming reports whether the device is asking for a yes or no.
func (l line) confirming() bool { return confirming.MatchString(l.cursor) }

// typing is what send_keys has typed since Enter was last pressed, so a
// line can be checked even before the device echoes it.
type typing struct {
	text    string
	unknown bool // up, down or tab changed the line in ways bauddy can't follow
	at      int  // the console's prompt count when typing began
}

// add records sending text and then key. prompts is the console's prompt
// count: a prompt since typing began means the device took the line.
func (t *typing) add(text, key string, prompts int) {
	if t.at != prompts {
		*t = typing{at: prompts}
	}
	t.text += strings.ReplaceAll(text, "?", "") // the device answers ? and retypes the rest
	switch key {
	case "space":
		t.text += " "
	case "backspace":
		if _, n := utf8.DecodeLastRuneInString(t.text); n > 0 {
			t.text = t.text[:len(t.text)-n]
		}
	case "tab", "up", "down":
		t.unknown = true
	case "enter", "ctrl+u", "ctrl+c", "ctrl+z":
		*t = typing{at: prompts}
	}
}

// pending returns what's typed and not yet entered, as of prompt count
// prompts, and whether up, down or tab changed it.
func (t typing) pending(prompts int) (string, bool) {
	if t.at != prompts {
		return "", false
	}
	return t.text, t.unknown
}

// safeAnswer reports whether sending text and key at a yes-or-no question
// can only decline or stop it.
func safeAnswer(text, key string) bool {
	if no := strings.EqualFold(text, "n") || strings.EqualFold(text, "no"); no {
		return key == "" || key == "enter"
	}
	return text == "" && slices.Contains([]string{"ctrl+c", "ctrl+z", "ctrl+shift+6", "esc", "ctrl+u", "backspace"}, key)
}

// target is the device an approval is for, and where to note the answer.
type target struct {
	name, path string
	mark       func(string) // writes to the log
}

func (c *console) target() target { return target{c.name, c.port.Path(), c.Mark} }

// approvalID names the approval among a call's input requests.
const approvalID = "approve"

// askedMax bounds approvals awaiting an answer.
const askedMax = 16

// approve checks that the user approved sending what, which needs it for
// the reasons in whys. The agent can't answer for them: the first time,
// approve returns a result that has the client ask the user, and the client
// then repeats the call with their answer, which is honoured once, for that
// same call, text and port only. A client that can't ask is refused unless
// the server was started with AllowRisky. A nil result and error means go
// ahead.
func (s *server) approve(req *mcp.CallToolRequest, dev target, what string, whys []string) (*mcp.CallToolResult, error) {
	call, err := callKey(req.Params)
	if err != nil {
		return nil, err
	}
	call += "\x00" + dev.path + "\x00" + what
	if state := req.Params.RequestState; state != "" {
		asked, ok := s.asked[state]
		delete(s.asked, state)
		if !ok || asked != call {
			return nil, errors.New("refused, so nothing was sent: that approval has expired or was for a different call; call again to ask the user")
		}
		if r, ok := req.Params.InputResponses[approvalID].(*mcp.ElicitResult); ok && r.Action == "accept" && r.Content["allow"] == true {
			dev.mark("user approved: " + what)
			return nil, nil
		}
		dev.mark("user refused: " + what)
		return nil, errors.New("the user refused, so nothing was sent. Don't send it another way; ask the user how they want to go on")
	}

	slices.Sort(whys)
	why := strings.Join(slices.Compact(whys), "; it ")
	if e := req.ClientCapabilities(); e == nil || e.Elicitation == nil || (e.Elicitation.Form == nil && e.Elicitation.URL != nil) {
		if s.opt.AllowRisky {
			dev.mark("sent without asking (--allow-risky): " + what)
			return nil, nil
		}
		return nil, fmt.Errorf("refused, so nothing was sent: this needs the user's approval, since it %s, and this MCP "+
			"client can't ask for it. Tell the user what you wanted to send and why; they can type it themselves in bauddy", why)
	}
	if len(s.asked) >= askedMax {
		clear(s.asked) // calls the client never repeated
	}
	state := rand.Text()
	s.asked[state] = call
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{approvalID: &mcp.ElicitParams{
			Message: fmt.Sprintf("An AI agent wants to send this to %s (%s):\n\n%s\n\nIt %s.", dev.name, dev.path, what, why),
			RequestedSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"allow": {Type: "boolean", Title: "Send it to the device", Default: json.RawMessage("false")},
				},
				Required: []string{"allow"},
			},
		}},
		RequestState: state,
	}, nil
}

// callKey identifies a call by its tool and arguments, with the arguments
// re-encoded so that key order and spacing don't matter.
func callKey(p *mcp.CallToolParamsRaw) (string, error) {
	var args any
	if len(p.Arguments) > 0 {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return "", err
		}
	}
	b, err := json.Marshal(args)
	return p.Name + "\x00" + string(b), err
}
