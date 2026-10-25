package probe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// newKeyProber builds a Prober for the tests in this file. The fake client is
// never called: encoding a data key is a local operation.
func newKeyProber(t *testing.T) *Prober {
	t.Helper()

	p, err := New(Options{
		Client:        &stellartest.Fake{},
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)
	return p
}

func testContract(t *testing.T) xdr.ScAddress {
	t.Helper()

	contract, err := stellar.ParseContractID(stellartest.SACContract)
	require.NoError(t, err)
	return contract
}

// decodeLedgerKey reads back a base64 ledger key, so a test can assert on
// what was encoded rather than on the encoding of it.
func decodeLedgerKey(t *testing.T, encoded string) xdr.LedgerKey {
	t.Helper()

	var key xdr.LedgerKey
	require.NoError(t, xdr.SafeUnmarshalBase64(encoded, &key))
	return key
}

func TestEncodeDataKeysRejectsAnInvalidSpec(t *testing.T) {
	t.Parallel()

	p := newKeyProber(t)
	contract := testContract(t)

	// The error must name the offending spec: a --key that cannot be
	// encoded is otherwise indistinguishable from a missing entry.
	_, err := p.encodeDataKeys(contract, []string{"sym:Balance", "u32:not-a-number"}, health.DurabilityPersistent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "u32:not-a-number")
}

func TestEncodeDataKeysRejectsAnInvalidSpecOnItsOwn(t *testing.T) {
	t.Parallel()

	p := newKeyProber(t)
	contract := testContract(t)

	_, err := p.encodeDataKeys(contract, []string{"sym:has spaces"}, health.DurabilityPersistent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sym:has spaces")
}

func TestEncodeDataKeysDurabilityChangesTheKey(t *testing.T) {
	t.Parallel()

	p := newKeyProber(t)
	contract := testContract(t)

	persistent, err := p.encodeDataKeys(contract, []string{"sym:Balance"}, health.DurabilityPersistent)
	require.NoError(t, err)
	require.Len(t, persistent, 1)

	temporary, err := p.encodeDataKeys(contract, []string{"sym:Balance"}, health.DurabilityTemporary)
	require.NoError(t, err)
	require.Len(t, temporary, 1)

	// Durability is part of the ledger key, so the same spec at a different
	// durability addresses a different entry — reading one while inspecting
	// the other would silently report the wrong entry's health.
	assert.NotEqual(t, persistent[0].encoded, temporary[0].encoded)

	persistentKey := decodeLedgerKey(t, persistent[0].encoded)
	temporaryKey := decodeLedgerKey(t, temporary[0].encoded)

	assert.Equal(t, xdr.LedgerEntryTypeContractData, persistentKey.Type)
	assert.Equal(t, xdr.ContractDataDurabilityPersistent, persistentKey.ContractData.Durability)
	assert.Equal(t, xdr.ContractDataDurabilityTemporary, temporaryKey.ContractData.Durability)
}

func TestEncodeDataKeysEncodesTheSpecAsTheEntryKey(t *testing.T) {
	t.Parallel()

	p := newKeyProber(t)
	contract := testContract(t)

	keys, err := p.encodeDataKeys(contract, []string{"sym:Balance", "u32:7"}, health.DurabilityPersistent)
	require.NoError(t, err)
	require.Len(t, keys, 2)

	// The spec is reported back unchanged, so a caller can line the reports
	// up with the keys it asked for.
	assert.Equal(t, "sym:Balance", keys[0].spec)
	assert.Equal(t, "u32:7", keys[1].spec)

	balance := decodeLedgerKey(t, keys[0].encoded)
	require.NotNil(t, balance.ContractData)
	assert.Equal(t, contract, balance.ContractData.Contract)
	require.NotNil(t, balance.ContractData.Key.Sym)
	assert.Equal(t, xdr.ScSymbol("Balance"), *balance.ContractData.Key.Sym)

	seven := decodeLedgerKey(t, keys[1].encoded)
	require.NotNil(t, seven.ContractData)
	require.NotNil(t, seven.ContractData.Key.U32)
	assert.EqualValues(t, 7, *seven.ContractData.Key.U32)
}

func TestEncodeDataKeysKeepsDuplicateSpecsInOrder(t *testing.T) {
	t.Parallel()

	p := newKeyProber(t)
	contract := testContract(t)

	// Duplicates are not collapsed here: this function turns one spec into
	// one ledger key and keeps the order it was given. The CLI drops
	// duplicate specs before they reach it (see mergeKeys), but the API can
	// still repeat a query parameter, and repeating one must not change the
	// answer — the same spec always encodes to the same key, and the keys
	// come back in the order they were asked for.
	keys, err := p.encodeDataKeys(contract, []string{"sym:Balance", "sym:Admin", "sym:Balance"}, health.DurabilityPersistent)
	require.NoError(t, err)
	require.Len(t, keys, 3)

	assert.Equal(t, "sym:Balance", keys[0].spec)
	assert.Equal(t, "sym:Admin", keys[1].spec)
	assert.Equal(t, "sym:Balance", keys[2].spec)

	assert.Equal(t, keys[0].encoded, keys[2].encoded, "the same spec always encodes to the same key")
	assert.NotEqual(t, keys[0].encoded, keys[1].encoded)
}

func TestEncodeDataKeysIsDeterministic(t *testing.T) {
	t.Parallel()

	p := newKeyProber(t)
	contract := testContract(t)

	first, err := p.encodeDataKeys(contract, []string{"sym:Balance", "u32:7"}, health.DurabilityPersistent)
	require.NoError(t, err)
	second, err := p.encodeDataKeys(contract, []string{"sym:Balance", "u32:7"}, health.DurabilityPersistent)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestEncodeDataKeysWithNoSpecs(t *testing.T) {
	t.Parallel()

	p := newKeyProber(t)
	contract := testContract(t)

	keys, err := p.encodeDataKeys(contract, nil, health.DurabilityPersistent)
	require.NoError(t, err)
	assert.Empty(t, keys)
}
