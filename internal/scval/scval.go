// Package scval converts between human-friendly argument strings and Soroban
// ScVals, and renders ScVal results as JSON-friendly Go values.
//
// # Argument syntax
//
// Arguments use a "type:value" form, for example:
//
//	u32:5            i128:-1000000      bool:true
//	sym:transfer     str:"hello world"  bytes:deadbeef
//	addr:GABC...     addr:CDEF...       void
//
// An integer may carry a decimal scale, written as an "@" followed by the
// token's decimal count: "i128:10.5@7" is 10.5 units of a 7-decimal token,
// which is 105000000 base units.
//
// A bare value without a type prefix is inferred; see Registry.Encode for the
// exact rules. Explicit prefixes are always preferred, because a contract that
// expects u32 will fail confusingly if handed an i128.
//
// # Extending
//
// Encoders live in a Registry keyed by type name. To support a new type, write
// an EncodeFunc and register it:
//
//	reg := scval.NewRegistry()
//	reg.Register("myType", func(literal string) (xdr.ScVal, error) { ... })
//
// Decoding is handled by Decode, which switches on the ScVal type; adding a
// case there is the counterpart change.
package scval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Codec encodes call arguments and decodes call results.
//
// Both the CLI and the HTTP API depend on this interface rather than on a
// concrete implementation, so an alternative argument syntax can be dropped in
// without touching the probe layer.
type Codec interface {
	// Encode converts a single argument specification into an ScVal.
	Encode(spec string) (xdr.ScVal, error)
	// EncodeAll converts a list of argument specifications.
	EncodeAll(specs []string) ([]xdr.ScVal, error)
	// Decode converts an ScVal into a JSON-marshalable Go value.
	Decode(v xdr.ScVal) (any, error)
}

// EncodeFunc turns the value half of a "type:value" argument into an ScVal.
type EncodeFunc func(literal string) (xdr.ScVal, error)

// Registry is the default Codec. It maps type names onto EncodeFuncs.
//
// A Registry is safe for concurrent reads once built. Register must not be
// called concurrently with Encode.
type Registry struct {
	encoders map[string]EncodeFunc
}

var _ Codec = (*Registry)(nil)

// NewRegistry returns a Registry with the built-in encoders installed.
func NewRegistry() *Registry {
	r := &Registry{encoders: make(map[string]EncodeFunc)}
	registerBuiltins(r)
	registerCollections(r)
	return r
}

// Register installs an encoder for a type name, replacing any existing one.
// Names are matched case-insensitively.
func (r *Registry) Register(typeName string, fn EncodeFunc) {
	r.encoders[strings.ToLower(typeName)] = fn
}

// TypeNames lists every registered type name, sorted. Used by CLI help text.
func (r *Registry) TypeNames() []string {
	names := make([]string, 0, len(r.encoders))
	for name := range r.encoders {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Encode converts one argument specification into an ScVal.
//
// A spec of the form "type:value" uses the registered encoder for that type.
// A spec with no recognized type prefix is inferred, in this order:
//
//   - "true" or "false" become bool
//   - "void" or "null" become void
//   - a valid strkey account ("G...") or contract ("C...") becomes an address
//   - an optionally-signed run of digits becomes i128
//   - anything else becomes a symbol if it fits Soroban's symbol constraints
//     (at most 32 characters of [a-zA-Z0-9_]), and a string otherwise
//
// Inferring i128 for bare integers is deliberate: it is the widest common
// integer type in Soroban token interfaces, and narrowing to u32 silently would
// be the more damaging guess.
func (r *Registry) Encode(spec string) (xdr.ScVal, error) {
	if prefix, literal, ok := splitSpec(spec); ok {
		if fn, found := r.encoders[prefix]; found {
			v, err := fn(literal)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("argument %q: %w", spec, err)
			}
			return v, nil
		}
	}

	v, err := r.infer(spec)
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("argument %q: %w", spec, err)
	}
	return v, nil
}

// EncodeAll converts a list of argument specifications, reporting the index of
// the first one that fails.
func (r *Registry) EncodeAll(specs []string) ([]xdr.ScVal, error) {
	out := make([]xdr.ScVal, 0, len(specs))
	for i, spec := range specs {
		v, err := r.Encode(spec)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// Decode converts an ScVal into a JSON-marshalable Go value.
func (r *Registry) Decode(v xdr.ScVal) (any, error) { return Decode(v) }

// HasType reports whether spec names its type explicitly, either as a
// registered "type:value" prefix or as a bare type name such as "void". A spec
// for which HasType is false would be encoded by inference.
func (r *Registry) HasType(spec string) bool {
	if prefix, _, ok := splitSpec(spec); ok {
		if _, found := r.encoders[prefix]; found {
			return true
		}
	}
	switch strings.ToLower(spec) {
	case "void", "null":
		return true
	}
	return false
}

// splitSpec separates a "type:value" spec. A bare token with no colon, or one
// whose prefix contains characters that cannot start a type name, is not a spec.
func splitSpec(spec string) (prefix, literal string, ok bool) {
	idx := strings.Index(spec, ":")
	if idx <= 0 {
		return "", "", false
	}
	prefix = strings.ToLower(spec[:idx])
	// Reject anything that is obviously not a type name, so that values which
	// legitimately contain a colon (a URL, say) fall through to inference.
	for _, c := range prefix {
		isAlnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
		if !isAlnum {
			return "", "", false
		}
	}
	return prefix, spec[idx+1:], true
}
