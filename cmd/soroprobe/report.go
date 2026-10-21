package main

import (
	"fmt"
	"os"
)

// reportFile is a --report destination. It is created before any network call,
// so an unwritable path fails fast, and written once the result exists. A nil
// *reportFile is a no-op, which keeps call sites free of conditionals.
type reportFile struct {
	f *os.File
}

// openReport creates the report file at path. An empty path means no report.
func openReport(path string) (*reportFile, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create report file %s: %w", path, err)
	}
	return &reportFile{f: f}, nil
}

// write stores v as the indented JSON report, the same shape --json prints.
func (r *reportFile) write(v any) error {
	if r == nil {
		return nil
	}
	return writeJSON(r.f, v)
}

// close releases the file once the result has been written, or when the
// command fails before reaching that point.
func (r *reportFile) close() error {
	if r == nil {
		return nil
	}
	return r.f.Close()
}
