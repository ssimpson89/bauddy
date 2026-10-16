package share

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
)

// version is the protocol version. A bauddy speaking another version can't
// join; the user is told to close the other one.
const version = 1

// maxMsg caps one message, so a confused peer can't make us buffer
// without limit. The largest message is the replay: 64 KB of output, base64.
const maxMsg = 256 << 10

// Message types.
const (
	tHello   = "hello"   // client → hub, first: who it is
	tData    = "data"    // hub → client: device output
	tState   = "state"   // hub → client: Status changed
	tNote    = "note"    // hub → windows: something to show on the status line
	tMark    = "mark"    // hub → windows: a line for history and the log
	tStopped = "stopped" // hub → agent: the user stopped it
	tWrite   = "write"   // client → hub: bytes for the device
	tKey     = "key"     // window → hub: the user pressed a key
	tReq     = "req"     // client → hub: an operation, answered by tReply
	tReply   = "reply"   // hub → client
	tBye     = "bye"     // hub → client: refused; Text says why
)

// Operations a client can ask for.
const (
	opSetBaud   = "setbaud"
	opBreak     = "break"
	opDetect    = "detect"
	opReconnect = "reconnect"
	opLease     = "lease"   // agent: take the keyboard
	opRelease   = "release" // agent: give it back
	opStop      = "stop"    // window: stop the agent (Interrupt: and send ctrl+c)
	opPause     = "pause"   // window: pause the agent, or let it type again
)

// msg is one message on the socket: a line of JSON. Only the fields its
// type uses are set.
type msg struct {
	T string `json:"t"`

	Version int    `json:"v,omitempty"`      // hello
	Agent   bool   `json:"agent,omitempty"`  // hello: an AI tool, not a window
	Name    string `json:"name,omitempty"`   // hello: the AI tool's name
	Replay  bool   `json:"replay,omitempty"` // hello: send recent output; data: this is it
	Paused  bool   `json:"paused,omitempty"` // hello: the user had paused the AI tool
	Hub     string `json:"hub,omitempty"`    // hello: the hub this client was on before

	Data  []byte  `json:"data,omitempty"`  // data, write
	Text  string  `json:"text,omitempty"`  // note, mark, stopped, bye
	State *Status `json:"state,omitempty"` // state

	ID          int     `json:"id,omitempty"` // req, reply
	Op          string  `json:"op,omitempty"`
	Baud        int     `json:"baud,omitempty"`
	Probe       bool    `json:"probe,omitempty"`
	KeepFraming bool    `json:"keep,omitempty"`
	Interrupt   bool    `json:"interrupt,omitempty"`
	Err         string  `json:"err,omitempty"`
	Result      *Result `json:"result,omitempty"` // detect's
}

// Status is a shared console as everyone sees it.
type Status struct {
	Connected bool   `json:"connected"`
	Baud      int    `json:"baud"`
	Framing   string `json:"framing"`
	Windows   int    `json:"windows"`   // bauddy windows on it
	Agent     string `json:"agent"`     // the connected AI tool's name, or ""
	Working   bool   `json:"working"`   // the AI tool has the keyboard
	Paused    bool   `json:"paused"`    // the user paused the AI tool
	Detecting bool   `json:"detecting"` // baud detection is running
	Hub       string `json:"hub"`       // which hub: it changes when another bauddy takes over the port
}

// Result is what detection found, as sent over the socket.
type Result struct {
	Baud       int    `json:"baud"`
	Framing    string `json:"framing"`
	StrayLogin bool   `json:"strayLogin,omitempty"`
}

// wire reads and writes messages on one connection. Writes may come from
// several goroutines.
type wire struct {
	r  *bufio.Reader
	wm sync.Mutex
	w  io.Writer
}

func newWire(rw io.ReadWriter) *wire {
	return &wire{r: bufio.NewReaderSize(rw, maxMsg), w: rw}
}

func (c *wire) send(m msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wm.Lock()
	defer c.wm.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// recv reads the next message. A line longer than maxMsg is an error.
func (c *wire) recv() (msg, error) {
	var m msg
	line, err := c.r.ReadSlice('\n')
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(line, &m)
}
