package scval

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// maxSymbolLen is Soroban's limit on ScSymbol length.
const maxSymbolLen = 32

func registerBuiltins(r *Registry) {
	r.Register("bool", encodeBool)
	r.Register("void", func(string) (xdr.ScVal, error) { return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil })
	r.Register("null", func(string) (xdr.ScVal, error) { return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil })

	r.Register("u32", encodeU32)
	r.Register("i32", encodeI32)
	r.Register("u64", encodeU64)
	r.Register("i64", encodeI64)
	r.Register("u128", encodeU128)
	r.Register("i128", encodeI128)
	r.Register("u256", encodeU256)
	r.Register("i256", encodeI256)

	r.Register("timepoint", encodeTimepoint)
	r.Register("duration", encodeDuration)

	r.Register("sym", encodeSymbol)
	r.Register("symbol", encodeSymbol)
	r.Register("str", encodeString)
	r.Register("string", encodeString)
	r.Register("bytes", encodeBytes)
	r.Register("addr", encodeAddress)
	r.Register("address", encodeAddress)
}

// infer applies the documented inference rules for a bare argument.
func (r *Registry) infer(spec string) (xdr.ScVal, error) {
	switch strings.ToLower(spec) {
	case "true", "false":
		return encodeBool(spec)
	case "void", "null":
		return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
	}

	if strkey.IsValidEd25519PublicKey(spec) || strkey.IsValidContractAddress(spec) {
		return encodeAddress(spec)
	}

	if isIntegerLiteral(spec) {
		return encodeI128(spec)
	}

	if isSymbolLiteral(spec) {
		return encodeSymbol(spec)
	}
	return encodeString(spec)
}

func isIntegerLiteral(s string) bool {
	if s == "" {
		return false
	}
	body := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	if body == "" {
		return false
	}
	for _, c := range body {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isSymbolLiteral(s string) bool {
	if s == "" || len(s) > maxSymbolLen {
		return false
	}
	for _, c := range s {
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
		if !isAlnum {
			return false
		}
	}
	return true
}

// unquote strips one layer of matching single or double quotes, which lets a
// shell-quoted string keep leading or trailing whitespace.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func encodeBool(literal string) (xdr.ScVal, error) {
	b, err := strconv.ParseBool(strings.TrimSpace(literal))
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("expected a boolean, got %q", literal)
	}
	return xdr.ScVal{Type: xdr.ScValTypeScvBool, B: &b}, nil
}

func encodeU32(literal string) (xdr.ScVal, error) {
	n, err := parseInteger(literal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	if err := checkRange(n, 32, false, "u32"); err != nil {
		return xdr.ScVal{}, err
	}
	v := xdr.Uint32(n.Uint64()) //nolint:gosec // checkRange bounded n to an unsigned 32-bit value.
	return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &v}, nil
}

func encodeI32(literal string) (xdr.ScVal, error) {
	n, err := parseInteger(literal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	if err := checkRange(n, 32, true, "i32"); err != nil {
		return xdr.ScVal{}, err
	}
	v := xdr.Int32(n.Int64()) //nolint:gosec // checkRange bounded n to a signed 32-bit value.
	return xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: &v}, nil
}

func encodeU64(literal string) (xdr.ScVal, error) {
	n, err := parseInteger(literal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	if err := checkRange(n, 64, false, "u64"); err != nil {
		return xdr.ScVal{}, err
	}
	v := xdr.Uint64(n.Uint64())
	return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &v}, nil
}

func encodeI64(literal string) (xdr.ScVal, error) {
	n, err := parseInteger(literal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	if err := checkRange(n, 64, true, "i64"); err != nil {
		return xdr.ScVal{}, err
	}
	v := xdr.Int64(n.Int64())
	return xdr.ScVal{Type: xdr.ScValTypeScvI64, I64: &v}, nil
}

func encodeTimepoint(literal string) (xdr.ScVal, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(literal), 10, 64)
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("expected a unix timestamp, got %q", literal)
	}
	v := xdr.TimePoint(n)
	return xdr.ScVal{Type: xdr.ScValTypeScvTimepoint, Timepoint: &v}, nil
}

func encodeDuration(literal string) (xdr.ScVal, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(literal), 10, 64)
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("expected a duration in seconds, got %q", literal)
	}
	v := xdr.Duration(n)
	return xdr.ScVal{Type: xdr.ScValTypeScvDuration, Duration: &v}, nil
}

func encodeSymbol(literal string) (xdr.ScVal, error) {
	s := unquote(literal)
	if len(s) > maxSymbolLen {
		return xdr.ScVal{}, fmt.Errorf("symbol %q is %d characters, limit is %d", s, len(s), maxSymbolLen)
	}
	if !isSymbolLiteral(s) && s != "" {
		return xdr.ScVal{}, fmt.Errorf("symbol %q may only contain a-z, A-Z, 0-9 and underscore", s)
	}
	v := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &v}, nil
}

func encodeString(literal string) (xdr.ScVal, error) {
	v := xdr.ScString(unquote(literal))
	return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &v}, nil
}

func encodeBytes(literal string) (xdr.ScVal, error) {
	s := strings.TrimPrefix(strings.TrimSpace(unquote(literal)), "0x")
	raw, err := hex.DecodeString(s)
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("expected hex-encoded bytes, got %q: %w", literal, err)
	}
	v := xdr.ScBytes(raw)
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &v}, nil
}

func encodeAddress(literal string) (xdr.ScVal, error) {
	s := strings.TrimSpace(unquote(literal))

	switch {
	case strkey.IsValidEd25519PublicKey(s):
		raw, err := strkey.Decode(strkey.VersionByteAccountID, s)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("decode account address %q: %w", s, err)
		}
		var key xdr.Uint256
		copy(key[:], raw)
		addr := xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &xdr.AccountId{Type: xdr.PublicKeyTypePublicKeyTypeEd25519, Ed25519: &key},
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr}, nil

	case strkey.IsValidContractAddress(s):
		raw, err := strkey.Decode(strkey.VersionByteContract, s)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("decode contract address %q: %w", s, err)
		}
		var id xdr.ContractId
		copy(id[:], raw)
		addr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &id}
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr}, nil

	default:
		return xdr.ScVal{}, fmt.Errorf("expected an account (G...) or contract (C...) address, got %q", s)
	}
}

// --- 128 and 256 bit integers ---------------------------------------------

// maxDecimals bounds the decimal count of a scaled amount. Ten to the
// seventy-seventh does not fit in a 256-bit integer, so no amount SoroProbe
// could encode has more decimals than this, and the bound keeps the scaling
// arithmetic cheap.
const maxDecimals = 76

// parseInteger parses an integer literal into a big.Int.
//
// The literal may carry a decimal scale, written as an "@" followed by the
// token's decimal count: "10.5@7" is 10.5 units of a 7-decimal token, which
// is 105000000 base units. The scaling is exact — the integer and fractional
// parts are combined with big.Int arithmetic, never with floating point — so
// no amount is ever silently off by a rounding error.
//
// A fractional part with more digits than the decimal count is rejected
// rather than rounded, because a rounded amount is a different amount.
func parseInteger(literal string) (*big.Int, error) {
	s := strings.TrimSpace(literal)
	if s == "" {
		return nil, fmt.Errorf("expected an integer, got %q", literal)
	}

	value, scale, hasScale := strings.Cut(s, "@")
	if !hasScale {
		return parseWholeNumber(value, literal)
	}

	decimals, ok := new(big.Int).SetString(scale, 10)
	if !ok || decimals.Sign() < 0 {
		return nil, fmt.Errorf("expected a decimal count after \"@\", got %q", scale)
	}
	if !decimals.IsUint64() || decimals.Uint64() > maxDecimals {
		return nil, fmt.Errorf("decimal count %s is out of range (0-%d)", decimals, maxDecimals)
	}
	return scaleByDecimals(value, decimals.Uint64(), literal)
}

// parseWholeNumber parses a plain base-10 integer.
func parseWholeNumber(s, literal string) (*big.Int, error) {
	if strings.ContainsRune(s, '.') {
		return nil, fmt.Errorf(
			"expected an integer, got %q (write %q to scale a token amount by its decimal count)",
			literal, literal+"@7")
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("expected a base-10 integer, got %q", literal)
	}
	return n, nil
}

// scaleByDecimals converts a decimal amount into base units.
//
// The value is split into its integer and fractional parts, the fraction is
// padded out to the token's decimal count, and the two are concatenated into
// a single integer: 10.5 at 7 decimals becomes the digits "10" and "5000000",
// which is 105000000. Working on the digits rather than on a float keeps the
// conversion exact for any number of digits.
func scaleByDecimals(value string, decimals uint64, literal string) (*big.Int, error) {
	negative := false
	switch {
	case strings.HasPrefix(value, "-"):
		negative = true
		value = value[1:]
	case strings.HasPrefix(value, "+"):
		value = value[1:]
	}

	intPart, fracPart, _ := strings.Cut(value, ".")
	if intPart == "" && fracPart == "" {
		return nil, fmt.Errorf("expected a number, got %q", literal)
	}
	if !isDigits(intPart) || !isDigits(fracPart) {
		return nil, fmt.Errorf("expected a number, got %q", literal)
	}
	if uint64(len(fracPart)) > decimals {
		return nil, fmt.Errorf(
			"amount %q has %d fractional digits but the token has %d decimals, so it cannot be represented exactly",
			literal, len(fracPart), decimals)
	}

	digits := intPart + fracPart + strings.Repeat("0", int(decimals)-len(fracPart)) //nolint:gosec // decimals is bounded by maxDecimals in parseInteger.
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, fmt.Errorf("expected a number, got %q", literal)
	}
	if negative {
		n.Neg(n)
	}
	return n, nil
}

// isDigits reports whether s is a run of decimal digits. The empty string is
// a run of zero digits, which is how an absent integer or fractional part is
// spelled.
func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// checkRange rejects values that do not fit the target width.
func checkRange(n *big.Int, bits uint, signed bool, name string) error {
	var lo, hi *big.Int
	if signed {
		hi = new(big.Int).Lsh(big.NewInt(1), bits-1)
		lo = new(big.Int).Neg(hi)
		hi.Sub(hi, big.NewInt(1))
	} else {
		lo = big.NewInt(0)
		hi = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), bits), big.NewInt(1))
	}
	if n.Cmp(lo) < 0 || n.Cmp(hi) > 0 {
		return fmt.Errorf("value %s is out of range for %s", n.String(), name)
	}
	return nil
}

// twosComplementWords renders n as `count` 64-bit words, most significant
// first, using two's complement for negative values.
func twosComplementWords(n *big.Int, count int) []uint64 {
	bits := uint(count * 64)
	v := new(big.Int).Set(n)
	if v.Sign() < 0 {
		v.Add(v, new(big.Int).Lsh(big.NewInt(1), bits))
	}

	words := make([]uint64, count)
	mask := new(big.Int).SetUint64(^uint64(0))
	tmp := new(big.Int)
	for i := count - 1; i >= 0; i-- {
		words[i] = tmp.And(v, mask).Uint64()
		v.Rsh(v, 64)
	}
	return words
}

func encodeU128(literal string) (xdr.ScVal, error) {
	n, err := parseInteger(literal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	if err := checkRange(n, 128, false, "u128"); err != nil {
		return xdr.ScVal{}, err
	}
	w := twosComplementWords(n, 2)
	parts := xdr.UInt128Parts{Hi: xdr.Uint64(w[0]), Lo: xdr.Uint64(w[1])}
	return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &parts}, nil
}

func encodeI128(literal string) (xdr.ScVal, error) {
	n, err := parseInteger(literal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	if err := checkRange(n, 128, true, "i128"); err != nil {
		return xdr.ScVal{}, err
	}
	w := twosComplementWords(n, 2)
	parts := xdr.Int128Parts{Hi: xdr.Int64(w[0]), Lo: xdr.Uint64(w[1])} //nolint:gosec // w holds the two's complement bit pattern; the conversion preserves bits.
	return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &parts}, nil
}

func encodeU256(literal string) (xdr.ScVal, error) {
	n, err := parseInteger(literal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	if err := checkRange(n, 256, false, "u256"); err != nil {
		return xdr.ScVal{}, err
	}
	w := twosComplementWords(n, 4)
	parts := xdr.UInt256Parts{
		HiHi: xdr.Uint64(w[0]), HiLo: xdr.Uint64(w[1]),
		LoHi: xdr.Uint64(w[2]), LoLo: xdr.Uint64(w[3]),
	}
	return xdr.ScVal{Type: xdr.ScValTypeScvU256, U256: &parts}, nil
}

func encodeI256(literal string) (xdr.ScVal, error) {
	n, err := parseInteger(literal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	if err := checkRange(n, 256, true, "i256"); err != nil {
		return xdr.ScVal{}, err
	}
	w := twosComplementWords(n, 4)
	parts := xdr.Int256Parts{
		HiHi: xdr.Int64(w[0]), HiLo: xdr.Uint64(w[1]), //nolint:gosec // w holds the two's complement bit pattern; the conversion preserves bits.
		LoHi: xdr.Uint64(w[2]), LoLo: xdr.Uint64(w[3]),
	}
	return xdr.ScVal{Type: xdr.ScValTypeScvI256, I256: &parts}, nil
}
