package scval_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/scval"
)

func testBook(t *testing.T) *scval.AddressBook {
	t.Helper()
	book, err := scval.NewAddressBook(map[string]string{
		"alice": testAccount,
		"token": testContract,
	})
	require.NoError(t, err)
	return book
}

func TestAddressBookResolvesNamedAddresses(t *testing.T) {
	t.Parallel()

	book := testBook(t)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"account with addr prefix", "addr:@alice", "addr:" + testAccount},
		{"contract with address prefix", "address:@token", "address:" + testContract},
		{"bare name gains an addr prefix", "@alice", "addr:" + testAccount},
		{"names may be written with the @ in the book", "addr:@token", "addr:" + testContract},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := book.Resolve(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAddressBookLeavesOrdinaryArgumentsAlone(t *testing.T) {
	t.Parallel()

	book := testBook(t)
	for _, spec := range []string{"u32:5", "sym:transfer", testAccount, "addr:" + testAccount, ""} {
		got, err := book.Resolve(spec)
		require.NoError(t, err)
		assert.Equal(t, spec, got)
	}
}

func TestAddressBookUnknownNameListsKnownNames(t *testing.T) {
	t.Parallel()

	_, err := testBook(t).Resolve("addr:@bob")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown address name "@bob"`)
	assert.Contains(t, err.Error(), "known: alice, token")
}

func TestAddressBookWithNoEntriesExplainsItself(t *testing.T) {
	t.Parallel()

	book, err := scval.NewAddressBook(nil)
	require.NoError(t, err)

	// The explicit form promises a lookup, so it must explain why it failed.
	_, err = book.Resolve("addr:@alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the address book is empty")

	// A bare "@..." is still an ordinary literal when no book is configured.
	got, err := book.Resolve("@alice")
	require.NoError(t, err)
	assert.Equal(t, "@alice", got)
}

func TestAddressBookRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries map[string]string
		wantErr string
	}{
		{
			name:    "not an address",
			entries: map[string]string{"alice": "not-an-address"},
			wantErr: "not a valid account",
		},
		{
			name:    "secret key",
			entries: map[string]string{"alice": "SAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
			wantErr: "not a valid account",
		},
		{
			name:    "empty name",
			entries: map[string]string{"@": testAccount},
			wantErr: "a name is required",
		},
		{
			name:    "name with a colon",
			entries: map[string]string{"bad:name": testAccount},
			wantErr: "must not contain",
		},
		{
			name:    "duplicate after normalising the @",
			entries: map[string]string{"alice": testAccount, "@alice": testContract},
			wantErr: "duplicate name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := scval.NewAddressBook(tt.entries)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestAddressBookNamesAreSorted(t *testing.T) {
	t.Parallel()

	book, err := scval.NewAddressBook(map[string]string{
		"token": testContract,
		"alice": testAccount,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "token"}, book.Names())
}

func TestAddressBookResolveAllReportsTheArgumentIndex(t *testing.T) {
	t.Parallel()

	_, _, err := testBook(t).ResolveAll([]string{"u32:1", "addr:@bob"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "arg 1")
	assert.Contains(t, err.Error(), `unknown address name "@bob"`)
}

func TestAddressBookResolveAllReportsWhetherAnythingChanged(t *testing.T) {
	t.Parallel()

	book := testBook(t)

	same, changed, err := book.ResolveAll([]string{"u32:1", "sym:transfer"})
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, []string{"u32:1", "sym:transfer"}, same)

	resolved, changed, err := book.ResolveAll([]string{"u32:1", "addr:@alice"})
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []string{"u32:1", "addr:" + testAccount}, resolved)
}
