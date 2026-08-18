// Package outputbudget trims long successful tool output into a recoverable
// head/tail receipt.
package outputbudget

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

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

// EstimatedTokens is a deterministic tokenizer-style counter used only to
// decide whether output should be budgeted or retired. It is intentionally
// local and dependency-free: hook execution must not fetch model tokenizer
// tables or block on network.
func EstimatedTokens(body string) int {
	tokens := 0
	for len(body) > 0 {
		r, size := utf8.DecodeRuneInString(body)
		if r == utf8.RuneError && size == 0 {
			break
		}

		switch {
		case unicode.IsSpace(r):
			body = body[size:]
			tokens++
		case isWordRune(r):
			n := size
			body = body[size:]
			for len(body) > 0 {
				next, nextSize := utf8.DecodeRuneInString(body)
				if !isWordRune(next) {
					break
				}
				n += nextSize
				body = body[nextSize:]
			}
			tokens += (n + 3) / 4
		case r < utf8.RuneSelf:
			body = body[size:]
			tokens++
		default:
			body = body[size:]
			tokens++
		}
	}
	return tokens
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
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
