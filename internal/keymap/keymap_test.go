package keymap

import "testing"

func TestKeysAreUnique(t *testing.T) {
	for _, esc := range []string{DefaultEscapeKey, "ctrl+t"} {
		seen := map[string]bool{}
		for _, b := range New(esc).Commands {
			if seen[b.Key] {
				t.Errorf("escape %s: key %q bound twice", esc, b.Key)
			}
			seen[b.Key] = true
		}
	}
}

func TestLookup(t *testing.T) {
	k := New(DefaultEscapeKey)
	cases := []struct {
		key  string
		want Action
	}{
		{"B", ActBreak},
		{"b", ActBaud},
		{DefaultEscapeKey, ActSendEscapeKey},
		{"z", ActNone},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			if got := k.Lookup(c.key); got != c.want {
				t.Errorf("Lookup(%q) = %v, want %v", c.key, got, c.want)
			}
		})
	}
}

func TestCheckEscape(t *testing.T) {
	for _, k := range []string{"ctrl+]", "ctrl+t", "ctrl+\\", "ctrl+a"} {
		t.Run(k, func(t *testing.T) {
			if err := CheckEscape(k); err != nil {
				t.Errorf("rejected: %v", err)
			}
		})
	}
	for _, k := range []string{"t", "esc", "ctrl+c", "ctrl+[", "alt+t", "ctrl+shift+t", "ctrl+T", "ctrl+1", "ctrl+@", "ctrl+/", "ctrl+ "} {
		t.Run(k, func(t *testing.T) {
			if CheckEscape(k) == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestGroupsSplitByWhatIsSent(t *testing.T) {
	g := New(DefaultEscapeKey).Groups()
	for _, b := range g[0].Bindings {
		if b.Sends {
			t.Errorf("%s sends to the device but is in %q", b.Key, g[0].Title)
		}
	}
	for _, b := range g[1].Bindings {
		if !b.Sends {
			t.Errorf("%s doesn't send but is in %q", b.Key, g[1].Title)
		}
	}
}

// A key that sends to the device can't run while detection has the port.
func TestKeysThatSendUseThePort(t *testing.T) {
	for _, b := range New(DefaultEscapeKey).Commands {
		if b.Sends && !b.Action.UsesPort() {
			t.Errorf("%s sends to the device but UsesPort is false", b.Key)
		}
	}
}
