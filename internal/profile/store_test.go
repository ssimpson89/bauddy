package profile

import "testing"

func TestRememberBaudMergesConcurrentWriters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Load() // a second bauddy, loaded before a saves

	if err := a.RememberLine("usb:A", 9600, "7E1"); err != nil {
		t.Fatal(err)
	}
	if err := b.RememberLine("usb:B", 115200, "8N1"); err != nil {
		t.Fatal(err)
	}

	c, _ := Load()
	if d := c.Adapter("usb:A"); d.Baud != 9600 || d.Framing != "7E1" {
		t.Errorf("usb:A = %d, want 9600 (lost to the second writer)", d.Baud)
	}
	if d := c.Adapter("usb:B"); d.Baud != 115200 {
		t.Errorf("usb:B = %d, want 115200", d.Baud)
	}
}

func TestRememberKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := Load()
	if err := s.RememberLine("usb:1", 9600, "8N1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberKeys("usb:1", true, true); err != nil {
		t.Fatal(err)
	}
	again, _ := Load()
	if a := again.Adapter("usb:1"); a.Baud != 9600 || !a.BackspaceH || !a.AddCR {
		t.Errorf("got %+v; want the rate kept and both fixes on", a)
	}
}
