package outputbudget

import (
	"strings"
	"testing"
)

func TestEstimatedTokensCountsLineHeavyOutputMoreThanRawByteProxy(t *testing.T) {
	body := strings.Repeat("x\n", 250)
	if got, oldProxy := EstimatedTokens(body), len(body)/4; got <= oldProxy {
		t.Fatalf("EstimatedTokens(%q...) = %d, want more than old len/4 proxy %d", body[:8], got, oldProxy)
	}
}

func TestBodyTokenThresholdBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	atThreshold := strings.Repeat("x\n", TokenThreshold/2)
	if got := EstimatedTokens(atThreshold); got != TokenThreshold {
		t.Fatalf("test fixture token count = %d, want %d", got, TokenThreshold)
	}
	if _, _, _, ok, err := Body(atThreshold, t.TempDir(), "sess1", testNotice); err != nil || ok {
		t.Fatalf("Body at threshold ok=%v err=%v, want untouched", ok, err)
	}

	overThreshold := atThreshold + "."
	if got := EstimatedTokens(overThreshold); got != TokenThreshold+1 {
		t.Fatalf("test fixture token count = %d, want %d", got, TokenThreshold+1)
	}
	if _, _, _, ok, err := Body(overThreshold, t.TempDir(), "sess1", testNotice); err != nil || !ok {
		t.Fatalf("Body over threshold ok=%v err=%v, want budgeted", ok, err)
	}
}

func testNotice(omitted int, archivePath string) string {
	return "omitted"
}
