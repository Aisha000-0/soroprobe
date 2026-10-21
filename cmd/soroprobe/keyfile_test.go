package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseKeyFileSkipsBlanksAndComments(t *testing.T) {
	t.Parallel()

	in := "" +
		"\n" +
		"   \n" +
		"# balances of every user\n" +
		"sym:Balance\n" +
		"  sym:Admin  \n" +
		"u32:7\n"

	keys, err := parseKeyFile(strings.NewReader(in), "keys.txt")
	require.NoError(t, err)
	assert.Equal(t, []string{"sym:Balance", "sym:Admin", "u32:7"}, keys)
}

func TestParseKeyFileEmptyFileYieldsNoKeys(t *testing.T) {
	t.Parallel()

	keys, err := parseKeyFile(strings.NewReader("# nothing yet\n\n"), "keys.txt")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestParseKeyFileReportsTheLineOfABadSpec(t *testing.T) {
	t.Parallel()

	in := "sym:Balance\nu32:not-a-number\nsym:Admin\n"

	_, err := parseKeyFile(strings.NewReader(in), "keys.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keys.txt:2", "the error must point at the offending line")
	assert.Contains(t, err.Error(), "u32:not-a-number", "the error must quote the offending spec")
}

func TestMergeKeysCombinesFileAndFlagKeys(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "keys.txt")
	require.NoError(t, os.WriteFile(path, []byte("sym:Balance\nsym:Admin\n"), 0o600))

	keys, err := mergeKeys(path, []string{"sym:Admin", "sym:Owner"})
	require.NoError(t, err)
	assert.Equal(t, []string{"sym:Balance", "sym:Admin", "sym:Owner"}, keys,
		"file keys come first, then flag keys, duplicates dropped")
}

func TestMergeKeysWithoutFileKeepsFlagKeys(t *testing.T) {
	t.Parallel()

	keys, err := mergeKeys("", []string{"sym:Admin", "sym:Admin"})
	require.NoError(t, err)
	assert.Equal(t, []string{"sym:Admin"}, keys)
}

func TestMergeKeysReportsAMissingFile(t *testing.T) {
	t.Parallel()

	_, err := mergeKeys(filepath.Join(t.TempDir(), "nope.txt"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope.txt")
}

func TestInvalidKeyFileFailsBeforeAnyNetworkCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.txt")
	require.NoError(t, os.WriteFile(path, []byte("sym:Balance\nu32:not-a-number\n"), 0o600))

	err := runWith(t, "inspect", "CDLZ", "--key-file", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keys.txt:2")
}

func TestCheckRejectsKeyFileWithFileBatch(t *testing.T) {
	// --file entries carry their own data_keys, so --key-file has no meaning
	// there; rejecting it beats silently ignoring it.
	err := runWith(t, "check", "--file", "-", "--key-file", "keys.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--key-file")
}
