// Command bauddy is a serial console for servers and network gear.
package main

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/ssimpson89/bauddy/internal/cli"
)

// Set by GoReleaser: -ldflags "-X main.version=1.2.3 -X main.commit=abc1234".
var (
	version = ""
	commit  = ""
)

func main() {
	if err := cli.NewRootCmd(buildVersion()).Execute(); err != nil {
		os.Exit(1)
	}
}

// buildVersion is the release version when GoReleaser set it, the module
// version for `go install ...@v1.2.3`, or "dev" plus the commit otherwise.
func buildVersion() string {
	v, c := version, commit
	if info, ok := debug.ReadBuildInfo(); ok {
		if mv := info.Main.Version; v == "" && mv != "" && mv != "(devel)" && !strings.Contains(mv, "-0.") {
			v = mv // a tagged module version; pseudo-versions like v0.0.0-2026...-abc are skipped
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && c == "" && len(s.Value) >= 7 {
				c = s.Value[:7]
			}
		}
	}
	switch {
	case v == "":
		v = "dev"
	case v[0] >= '0' && v[0] <= '9':
		v = "v" + v
	}
	if c != "" && v == "dev" {
		v += " (" + c + ")"
	}
	return v
}
