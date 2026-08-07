// Package cmdclass conservatively classifies Codex Bash commands for Winglet's
// investigate-to-implement phase tracking.
package cmdclass

import "strings"

// Class describes the phase signal a command carries.
type Class int

const (
	Neutral Class = iota
	Investigate
	Implement
)

// Classify returns a conservative phase classification for one shell command.
// It recognizes only simple read-only commands with no shell control
// operators. Mutating commands, package-manager commands, compounds,
// redirections, substitutions, and unknown prefixes stay Neutral. Bash commands
// never classify as Implement in v1.
func Classify(command string) Class {
	tokens, ok := simpleFields(command)
	if !ok || len(tokens) == 0 {
		return Neutral
	}

	switch tokens[0] {
	case "cat", "less", "head", "grep", "rg", "fd", "ls", "wc":
		return Investigate
	case "tail":
		if hasAnyFlag(tokens[1:], "-f", "--follow") {
			return Neutral
		}
		return Investigate
	case "find":
		if hasAnyFlag(tokens[1:], "-delete", "-exec", "-execdir", "-ok", "-okdir") {
			return Neutral
		}
		return Investigate
	case "git":
		return classifyGit(tokens[1:])
	case "go":
		return classifyGo(tokens[1:])
	case "curl":
		if hasAnyFlag(tokens[1:], "-o", "--output", "-O", "--remote-name", "-T", "--upload-file") {
			return Neutral
		}
		return Investigate
	case "wget":
		if hasAnyFlag(tokens[1:], "-O", "--output-document", "-P", "--directory-prefix") {
			return Neutral
		}
		return Investigate
	default:
		return Neutral
	}
}

func classifyGit(args []string) Class {
	if len(args) == 0 {
		return Neutral
	}
	switch args[0] {
	case "status", "diff", "log", "show", "grep":
		return Investigate
	default:
		return Neutral
	}
}

func classifyGo(args []string) Class {
	if len(args) == 0 || args[0] != "test" {
		return Neutral
	}
	if hasListFlag(args[1:]) {
		return Investigate
	}
	return Neutral
}

func hasListFlag(args []string) bool {
	for i, arg := range args {
		if arg == "-list" && i+1 < len(args) {
			return true
		}
		if strings.HasPrefix(arg, "-list=") && len(arg) > len("-list=") {
			return true
		}
	}
	return false
}

func hasAnyFlag(args []string, flags ...string) bool {
	for _, arg := range args {
		for _, flag := range flags {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return true
			}
		}
	}
	return false
}

func simpleFields(command string) ([]string, bool) {
	command = strings.TrimSpace(command)
	if command == "" || hasShellControl(command) {
		return nil, false
	}
	return strings.Fields(command), true
}

func hasShellControl(command string) bool {
	return strings.ContainsAny(command, "\n\r;&|<>`") || strings.Contains(command, "$(")
}
