// Package compactnudge centralizes the visible and model-facing /compact
// nudge text used by both hook integrations.
package compactnudge

const Message = "[agent-winglet] /compact nudge - you can compact the " +
	"session ahead of implementation, to save context while what's " +
	"still relevant is still clear."

const PreservationGuidance = " If the user chooses to compact, preserve: " +
	"the current task and constraints; files inspected or edited with relevant " +
	"symbols and line numbers; active decisions, hypotheses, and pending next " +
	"steps; commands and tests run, including failing error snippets. Drop or " +
	"summarize: successful verbose command output, repeated file reads, stale " +
	"search/listing output, and observations superseded by later edits."
