package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/soroworks/soroprobe/internal/scval"
)

// parseKeyFile reads key specs from r, one per line. Blank lines and lines
// whose first non-space character is '#' are ignored; a comment must own the
// whole line, because a spec such as str:"a # b" may legally contain '#'.
//
// Every spec is encoded with the same codec the prober uses, so an invalid
// line fails here — before any network call — reported with the line it sits
// on.
func parseKeyFile(r io.Reader, name string) ([]string, error) {
	codec := scval.NewRegistry()

	var keys []string
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		spec := strings.TrimSpace(sc.Text())
		if spec == "" || strings.HasPrefix(spec, "#") {
			continue
		}
		if _, err := codec.Encode(spec); err != nil {
			return nil, fmt.Errorf("key file %s:%d: invalid key spec %q: %w", name, line, spec, err)
		}
		keys = append(keys, spec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read key file %s: %w", name, err)
	}
	return keys, nil
}

// readKeyFile reads key specs from the file at path.
func readKeyFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseKeyFile(f, path)
}

// mergeKeys combines the specs in keyFile (empty means none) with the specs
// given via --key, file first, dropping duplicates so the same entry is not
// fetched twice.
func mergeKeys(keyFile string, flagKeys []string) ([]string, error) {
	var keys []string
	if keyFile != "" {
		fromFile, err := readKeyFile(keyFile)
		if err != nil {
			return nil, err
		}
		keys = append(keys, fromFile...)
	}
	keys = append(keys, flagKeys...)

	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out, nil
}
