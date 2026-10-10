package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestFitLineNeverExceedsWidth(t *testing.T) {
	layouts := []layout{
		{[]seg{{"core-sw-01  115200 ●  garbled? ctrl+] d", nil, false}}, []seg{{"ctrl+] menu ", nil, false}}},
		{[]seg{{"115200 ●", nil, false}}, []seg{{"ctrl+] ", nil, false}}},
	}
	for w := 1; w <= 80; w++ {
		if got := lipgloss.Width(fitLine(cBar, w, layouts)); got != w {
			t.Errorf("width %d: rendered %d cells", w, got)
		}
	}
}
