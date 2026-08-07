// Package outputbudget trims long successful tool output into a recoverable
// head/tail receipt.
package outputbudget

import (
	"fmt"
	"strings"

	"github.com/umitkaanusta/agent-winglet/internal/retire"
)

const (
	// TokenThreshold mirrors AgentDiet's token-based trigger (theta=500)
	// rather than a raw line count, which a caller can dodge just by wrapping
	// output onto fewer, longer lines.
	TokenThreshold = 500
	HeadLines      = 15
	TailLines      = 15
)

// EstimatedTokens is a cheap, tokenizer-free proxy (chars/4) used only to
// decide whether output should be budgeted.
func EstimatedTokens(body string) int {
	return len(body) / 4
}

// Body collapses body to its first HeadLines and last TailLines lines if its
// estimated token count exceeds TokenThreshold. The full original body is
// archived first, and the caller supplies the omission notice text.
func Body(body, root, sessionID string, notice func(omitted int, archivePath string) string) (budgeted string, omittedLines int, omittedBytes int64, ok bool, err error) {
	lines := strings.Split(body, "\n")
	trailingNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	n := len(lines)
	if EstimatedTokens(body) <= TokenThreshold {
		return "", 0, 0, false, nil
	}
	if n <= HeadLines+TailLines {
		return "", 0, 0, false, nil
	}

	archivePath, err := retire.Store(root, sessionID, []byte(body))
	if err != nil {
		return "", 0, 0, false, err
	}

	dropped := lines[HeadLines : n-TailLines]
	omittedLines = len(dropped)
	omittedBytes = int64(len(strings.Join(dropped, "\n")))

	var b strings.Builder
	b.WriteString(strings.Join(lines[:HeadLines], "\n"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "[agent-winglet] %s\n", notice(omittedLines, archivePath))
	b.WriteString(strings.Join(lines[n-TailLines:], "\n"))
	if trailingNewline {
		b.WriteString("\n")
	}
	return b.String(), omittedLines, omittedBytes, true, nil
}

// Stdout budgets successful shell stdout and includes the successful exit tag
// in the omission marker.
func Stdout(stdout, root, sessionID string) (budgeted string, omittedLines int, omittedBytes int64, ok bool, err error) {
	return Body(stdout, root, sessionID, func(omitted int, archivePath string) string {
		return fmt.Sprintf("%d lines omitted, exit 0 (showing first %d/last %d) - full output at %s",
			omitted, HeadLines, TailLines, archivePath)
	})
}

// TextField budgets freeform text without a shell outcome tag.
func TextField(body, root, sessionID string) (budgeted string, omittedLines int, omittedBytes int64, ok bool, err error) {
	return Body(body, root, sessionID, func(omitted int, archivePath string) string {
		return fmt.Sprintf("%d lines omitted (showing first %d/last %d) - full output at %s",
			omitted, HeadLines, TailLines, archivePath)
	})
}

// EntryList budgets a list of short entries by treating one entry as one line.
func EntryList(entries []string, root, sessionID string) (budgeted string, omittedEntries int, omittedBytes int64, ok bool, err error) {
	return Body(strings.Join(entries, "\n"), root, sessionID, func(omitted int, archivePath string) string {
		return fmt.Sprintf("%d entries omitted (showing first %d/last %d) - full list at %s",
			omitted, HeadLines, TailLines, archivePath)
	})
}
