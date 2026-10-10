// Command bauddy is a serial console for servers and network gear.
package main

import (
	"os"
	"regexp"
	"runtime/debug"

	"github.com/ssimpson89/bauddy/internal/cli"
)

// Set by GoReleaser: -ldflags "-X main.version=1.2.3 -X main.commit=abc1234".
var (
	version = ""
	commit  = ""
)

var (
	semver = regexp.MustCompile(`^\d+\.\d+\.\d+`)
	// pseudo matches versions Go makes up for untagged commits, such as
	// v0.0.0-20261010160946-61805dd34b0f, and local builds with changes (+dirty).
	pseudo = regexp.MustCompile(`\d{14}-[0-9a-f]{12}|\+dirty`)
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
		if mv := info.Main.Version; v == "" && mv != "" && mv != "(devel)" && !pseudo.MatchString(mv) {
			v = mv // a tagged module version, from `go install ...@v1.2.3`
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
	case semver.MatchString(v):
		v = "v" + v // GoReleaser passes 1.2.3
	}
	if c != "" && v == "dev" {
		v += " (" + c + ")"
	}
	return v
}
