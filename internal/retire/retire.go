// Package retire implements the achievable half of "retire used-up context
// at phase boundaries" (agent-winglet-v1-remaining.md §2.1, spec §4.2).
//
// The lever as originally specified — retroactively replacing
// investigate-phase tool output already sent earlier in the transcript with
// a compact receipt, the moment phase.Observe reports the investigate→
// implement crossing — is confirmed unbuildable in a hooks-only
// architecture: a PostToolUse hook's updatedToolOutput can only replace the
// result of the tool call it is currently processing, never an earlier
// one, and no other hook event can touch transcript content already sent
// (see agent-winglet-v1-remaining.md §2.1 for the full finding).
//
// What is achievable, using the same phase.Observe signal: once a session
// has already crossed the investigate→implement boundary, any further
// investigate-classified tool call (Read/Grep/Glob/WebFetch/WebSearch/Task)
// is retired at the moment it's produced — the one point in the pipeline a
// hook can act on — instead of replaying its full output. The raw content
// is preserved, content-addressed, on disk so nothing is destroyed, only
// deferred out of the transcript.
package retire

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

func dir(projectDir, sessionID string) string {
	return filepath.Join(projectDir, ".claude", "agent-winglet", sessionID+".retired")
}

// Store content-addresses content under the session's retired-content
// directory and returns the path it was written to. Storing the same
// content twice (e.g. an identical repeat investigate call) writes the same
// file, so it's naturally idempotent.
func Store(projectDir, sessionID string, content []byte) (path string, err error) {
	d := dir(projectDir, sessionID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	name := hex.EncodeToString(sum[:16]) + ".txt"
	path = filepath.Join(d, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Invalidate deletes all retired content for a session. Called on
// SessionStart and PostCompact, same as ledger.Invalidate and
// phase.Invalidate, so retired content never stands in as a substitute
// across a restart or compaction.
func Invalidate(projectDir, sessionID string) error {
	err := os.RemoveAll(dir(projectDir, sessionID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
