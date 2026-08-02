package harness

import (
	"bufio"
	"encoding/json"
	"os"
)

// AppendRecord appends one Record as a JSON line to path, creating the file
// if it doesn't exist. Never truncates — trials are meant to accumulate
// across many `measure` invocations over time, not just one run.
func AppendRecord(path string, r Record) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(r)
}

// ReadRecords reads all records from a JSONL results file.
func ReadRecords(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, scanner.Err()
}
