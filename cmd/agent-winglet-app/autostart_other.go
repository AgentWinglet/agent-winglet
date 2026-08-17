//go:build !linux

package main

// ensureLinuxTrayAutostart is a no-op outside Linux — darwin registers its
// login item via SMAppService (loginitem_darwin.go) and windows' installer
// writes its own Startup-folder shortcut (build/windows/installer/
// project.nsi), neither of which needs this XDG-specific mechanism. See
// autostart_linux.go for the real implementation.
func ensureLinuxTrayAutostart() {}
