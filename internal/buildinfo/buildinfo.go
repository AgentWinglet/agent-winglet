// Package buildinfo holds Version, the semantic version string the
// scripts/package/*.sh release scripts stamp into cmd/agent-winglet-app and
// cmd/agent-winglet-tray via `-ldflags "-X .../buildinfo.Version=..."` (see
// scripts/package-lib.sh's resolve_version for where that value comes from).
// A plain `make app`/`make tray` (or `go build ./...`) build leaves Version
// at "dev" — only the packaging scripts override it, so a dev build is
// always distinguishable from a released one.
package buildinfo

var Version = "dev"
