package harness

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndReadRecords_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")

	want := []Record{
		{Task: "fix-typo", Variant: "hook", Success: true, TotalCostUSD: 0.1234, NumTurns: 3, Timestamp: time.Now().UTC().Truncate(time.Second)},
		{Task: "fix-typo", Variant: "control", Success: false, TotalCostUSD: 0.2, NumTurns: 7, Timestamp: time.Now().UTC().Truncate(time.Second)},
	}
	for _, r := range want {
		if err := AppendRecord(path, r); err != nil {
			t.Fatalf("AppendRecord: %v", err)
		}
	}

	got, err := ReadRecords(path)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Task != want[i].Task || got[i].Variant != want[i].Variant ||
			got[i].Success != want[i].Success || got[i].TotalCostUSD != want[i].TotalCostUSD ||
			got[i].NumTurns != want[i].NumTurns || !got[i].Timestamp.Equal(want[i].Timestamp) {
			t.Errorf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAppendRecord_AccumulatesAcrossCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")

	for i := 0; i < 3; i++ {
		if err := AppendRecord(path, Record{Task: "t", Variant: "hook"}); err != nil {
			t.Fatalf("AppendRecord: %v", err)
		}
	}

	got, err := ReadRecords(path)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d records after 3 appends, want 3 (AppendRecord must not truncate)", len(got))
	}
}

func TestReadRecords_MissingFile(t *testing.T) {
	_, err := ReadRecords(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err == nil {
		t.Fatal("ReadRecords on a missing file: got nil error, want an error")
	}
}
