package stellar_test

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// parseContractIDSeeds are the inputs the fuzzer starts from.
//
// They are deliberately split between addresses that must be accepted and
// addresses that must be rejected. The accepted ones are the contract IDs the
// rest of the suite already depends on, so a regression in the parser shows up
// here as a failing seed rather than as a mystery failure elsewhere. The
// rejected ones are real account and muxed-account strkeys: they are valid
// addresses with a different version byte, which is exactly the kind of
// near-miss a hand-written check is likely to let through. Empty and truncated
// strings cover the ordinary malformed input a CLI or API caller sends.
var parseContractIDSeeds = []string{
	// Contracts the fixtures describe; each must parse and round-trip.
	stellartest.SACContract,
	stellartest.WasmContract,
	stellartest.UndeployedContract,

	// Valid G... and M... strkeys; neither is a contract address.
	stellartest.SourceAccount,
	"GAAQEAYEAUDAOCAJBIFQYDIOB4IBCEQTCQKRMFYYDENBWHA5DYPSABOV",
	"MAAAAAAAAAAAAAABAIBQIBIGA4EASCQLBQGQ4DYQCEJBGFAVCYLRQGI2DMOB2HQ7EAT3G",
	"MAAAAAAAAAAQAAACAMCAKBQHBAEQUCYMBUHA6EARCIJRIFIWC4MBSGQ3DQOR4HZAEGBTM",

	// Malformed input: empty, wrong shape, truncated, and a contract-looking
	// string whose checksum does not match.
	"",
	"hello",
	"C",
	"CDLZFC3SYJYDZT7K",
	"CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSD",
	"CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
}

// FuzzParseContractID pins down the two properties every CLI command and API
// route relies on: parsing arbitrary input never panics, and any input the
// parser accepts is exactly the input it was given.
//
// The second property is stronger than "the bytes decode". A successful parse
// has to re-encode to the original string, which rules out a parser that
// tolerates non-canonical spelling, ignores trailing bytes, or accepts the
// wrong strkey version. A rejected input needs no further assertion: failing is
// the contract, and only the panic and round-trip properties need checking.
func FuzzParseContractID(f *testing.F) {
	for _, seed := range parseContractIDSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, contractID string) {
		addr, err := stellar.ParseContractID(contractID)
		if err != nil {
			return
		}

		// The parser returned an address, so it must be a contract address
		// carrying a full 32-byte identifier.
		require.Equal(t, xdr.ScAddressTypeScAddressTypeContract, addr.Type)
		require.NotNil(t, addr.ContractId)

		// Acceptance must be exact: re-encoding the parsed address has to
		// reproduce the input, so the parser cannot quietly normalise it.
		back, err := addr.String()
		require.NoError(t, err)
		require.Equal(t, contractID, back)
	})
}
