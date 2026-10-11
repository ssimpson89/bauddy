package cli

import "testing"

func TestEscapeKey(t *testing.T) {
	t.Setenv("BAUDDY_ESCAPE", "ctrl+t")
	if got := escapeKey(""); got != "ctrl+t" {
		t.Errorf("env: got %q, want ctrl+t", got)
	}
	if got := escapeKey("ctrl+a"); got != "ctrl+a" {
		t.Errorf("flag beats env: got %q, want ctrl+a", got)
	}
	t.Setenv("BAUDDY_ESCAPE", "")
	if got := escapeKey(""); got != "ctrl+]" {
		t.Errorf("default: got %q, want ctrl+]", got)
	}
}
