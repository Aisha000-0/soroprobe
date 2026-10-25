package scval_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/scval"
)

// The collection parsers read JSON that arrived from a user, so they have to
// survive anything: a malformed body has to come back as an error rather than
// a panic, and a hostile body has to be rejected without spending real time
// on it. The fuzz target below pins both properties down, and the plain
// tests next to it cover the cases a fuzzer would take too long to stumble
// on.

// collectionBodies are the bodies that follow a "vec:" or "map:" prefix in
// collections_test.go, the valid spellings and the malformed ones alike. The
// fuzzer starts from every one of them, so a regression in a documented
// spelling shows up as a failing seed instead of as a lucky mutation.
var collectionBodies = []string{
	// vec:["u32:1", "u32:2"]
	`["u32:1", "u32:2"]`,
	// vec:[]
	`[]`,
	// vec:[5, true, null, "transfer"]
	`[5, true, null, "transfer"]`,
	// vec:[["u32:1"], []]
	`[["u32:1"], []]`,
	// vec:["vec:[\"u32:7\"]"]
	`["vec:[\"u32:7\"]"]`,
	// vec:["sym:a"]
	`["sym:a"]`,
	// map:[["sym:a", "u32:1"], ["sym:b", "u32:2"]]
	`[["sym:a", "u32:1"], ["sym:b", "u32:2"]]`,
	// map:[["u32:1", ["sym:x"]]]
	`[["u32:1", ["sym:x"]]]`,
	// map:[["sym:b", 1], ["sym:a", 2]]
	`[["sym:b", 1], ["sym:a", 2]]`,
	// vec:["answer:"] — a custom encoder inside a collection
	`["answer:"]`,
	// map:[["sym:a"]]
	`[["sym:a"]]`,
	// vec:1,2
	`1,2`,
	// vec:[1,
	`[1,`,
	// vec:[1.5]
	`[1.5]`,
	// vec:[{"a": 1}]
	`[{"a": 1}]`,
	// vec:["u32:-1"]
	`["u32:-1"]`,
	// map:[["sym:a", "u32:x"]]
	`["sym:a", "u32:x"]`,
	// map:{"a": 1}
	`{"a": 1}`,
}

// maxFuzzBodyBytes keeps the fuzzer's own work small. A body this long is
// already far past anything a real argument carries, and the HTTP API caps a
// request body at 64 KiB, so longer bodies are not reachable from untrusted
// input anyway.
const maxFuzzBodyBytes = 8 << 10

// FuzzCollections feeds arbitrary bodies to the vec and map parsers.
//
// Each body is tried after both prefixes, because the two parsers read the
// same JSON and share the same element rules. A body that does not parse has
// to come back as an error, and a body that does parse has to survive a
// round trip through the decoder.
//
// A panic in the parser fails the fuzz target, so "no panics" needs no
// assertion of its own: the engine reports it.
func FuzzCollections(f *testing.F) {
	for _, body := range collectionBodies {
		f.Add(body)
	}

	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > maxFuzzBodyBytes {
			t.Skip("body too long to keep the fuzzer cheap")
		}

		codec := scval.NewRegistry()
		for _, kind := range []string{"vec", "map"} {
			spec := kind + ":" + body

			v, err := codec.Encode(spec)
			if err != nil {
				// A failure has to say what went wrong; an empty error
				// would reach the user as a blank line.
				assert.NotEmpty(t, err.Error(), spec)
				continue
			}

			// A collection is a JSON array, so a body that does not
			// start with one cannot have encoded.
			if !strings.HasPrefix(strings.TrimSpace(body), "[") {
				t.Fatalf("Encode(%q) accepted a body that is not a JSON array", spec)
			}

			// What encoded has to decode again.
			_, err = codec.Decode(v)
			assert.NoError(t, err, spec)
		}
	})
}

// TestCollectionsMalformedBodiesFail checks that the malformed spellings
// from collections_test.go come back as errors rather than as values.
func TestCollectionsMalformedBodiesFail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind, body string
	}{
		{"vec", `1,2`},
		{"vec", `[1,`},
		{"vec", `[1.5]`},
		{"vec", `[{"a": 1}]`},
		{"vec", `["u32:-1"]`},
		{"vec", ``},
		{"map", `[["sym:a"]]`},
		{"map", `["sym:a", "u32:x"]`},
		{"map", `{"a": 1}`},
		{"map", ``},
	}
	codec := scval.NewRegistry()
	for _, tt := range tests {
		spec := tt.kind + ":" + tt.body
		_, err := codec.Encode(spec)
		require.Error(t, err, spec)
		assert.NotEmpty(t, err.Error(), spec)
	}
}

// TestCollectionsExtremeNestingIsRejected checks that a body nested far
// deeper than any contract type is turned away quickly instead of being
// parsed to the end. The parser re-reads the JSON of every level it descends
// into, so without a bound the work grows with the square of the depth.
func TestCollectionsExtremeNestingIsRejected(t *testing.T) {
	t.Parallel()

	codec := scval.NewRegistry()
	deep := strings.Repeat("[", 5000) + strings.Repeat("]", 5000)

	start := time.Now()
	_, err := codec.Encode("vec:" + deep)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nesting")
	assert.Less(t, elapsed, 5*time.Second, "a deeply nested body took %s to turn away", elapsed)
}

// TestCollectionsNestingJustInsideTheLimit checks that the nesting the
// parser still accepts keeps working, so the bound is a bound and not a ban.
func TestCollectionsNestingJustInsideTheLimit(t *testing.T) {
	t.Parallel()

	codec := scval.NewRegistry()
	body := `["u32:1"]`
	for i := 0; i < 8; i++ {
		body = "[" + body + "]"
	}

	v, err := codec.Encode("vec:" + body)
	require.NoError(t, err)
	require.Equal(t, xdr.ScValTypeScvVec, v.Type)
}

// TestCollectionsSpecInsideString checks the other way to nest: a collection
// written as a spec inside a string element. Every level has to escape the
// one below it, so an argument can only reach a handful of levels this way,
// but the parser has to handle the levels it can reach.
func TestCollectionsSpecInsideString(t *testing.T) {
	t.Parallel()

	spec := "vec:[]"
	for i := 0; i < 8; i++ {
		quoted, err := json.Marshal(spec)
		require.NoError(t, err)
		spec = "vec:[" + string(quoted) + "]"
	}

	codec := scval.NewRegistry()
	v, err := codec.Encode(spec)
	require.NoError(t, err)
	require.Equal(t, xdr.ScValTypeScvVec, v.Type)
}
