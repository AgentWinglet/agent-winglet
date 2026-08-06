// Package phase tracks, per Claude Code session, whether the session has
// crossed the investigate→implement boundary: at least one
// investigate-classified tool call, followed by the first
// implement-classified call.
//
// Claude Code exposes no hook mechanism to trigger compaction
// programmatically (PreCompact can only observe or block an already-pending
// compaction) — confirmed against the hooks reference, 2026-08-03 — so this
// package only ever surfaces a one-time signal for a hook to turn into a
// suggestion. It fires at most once per state lifetime, and — like the
// Session Ledger — that lifetime never survives a session restart or
// compaction: callers invalidate this state on SessionStart/PostCompact
// exactly as they do the Ledger's.
package phase

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/umitkaanusta/agent-winglet/internal/statedir"
)

type State struct {
	// Investigated is set once any investigate-classified tool call is seen.
	Investigated bool `json:"investigated"`
	// Suggested is set once the boundary-crossing signal has fired, so it
	// never fires more than once per state lifetime.
	Suggested bool `json:"suggested"`
	// InvestigateCalls counts every investigate-classified tool call seen
	// this session so far, independent of Suggested/Investigated — it keeps
	// counting after the boundary crosses, and it's what's checked
	// pre-boundary to decide when a session has been investigating long
	// enough that letting every further call's output accumulate raw would
	// grow context unboundedly (see cmd/ledger-hook's
	// investigateCallThreshold).
	InvestigateCalls int `json:"investigateCalls"`
}

func statePath(projectDir, sessionID string) (string, error) {
	d, err := statedir.Dir(projectDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, sessionID+".phase.json"), nil
}

func Load(projectDir, sessionID string) (*State, error) {
	p, err := statePath(projectDir, sessionID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return &State{}, nil
	}
	return &s, nil
}

func Save(projectDir, sessionID string, s *State) error {
	p, err := statePath(projectDir, sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Invalidate deletes the phase state for a session. Called on SessionStart
// and PostCompact, same as ledger.Invalidate, so a resumed or compacted
// session can cross the boundary again rather than staying silently
// suppressed by a signal fired in a part of the session that's now gone.
func Invalidate(projectDir, sessionID string) error {
	p, err := statePath(projectDir, sessionID)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Observe records one tool call's phase classification and reports whether
// this call crosses the investigate→implement boundary: the first
// implement-classified call after at least one investigate-classified call
// was already seen. isInvestigate and isImplement are mutually exclusive by
// construction (a tool call has one classification); a call that is neither
// (e.g. Bash, whose read-only-vs-mutating intent tool_name alone can't
// distinguish) leaves the state unchanged and always reports false.
func (s *State) Observe(isInvestigate, isImplement bool) (crossed bool) {
	if isInvestigate {
		s.Investigated = true
		s.InvestigateCalls++
		return false
	}
	if isImplement && s.Investigated && !s.Suggested {
		s.Suggested = true
		return true
	}
	return false
}
