package scval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stellar/go-stellar-sdk/strkey"
)

// AddressBook maps short names onto Stellar account ("G...") and contract
// ("C...") addresses, so an invocation can say addr:@alice instead of repeating
// a 56-character key across commands and check files.
//
// A name is written as "@name" in an argument. Resolution happens before
// encoding, in the probe layer, so the CLI, check files and HTTP API all
// behave identically.
type AddressBook struct {
	byName map[string]string
}

// NewAddressBook builds an AddressBook from a name-to-address map.
//
// Keys may be written with or without a leading "@": "alice" and "@alice" are
// the same name. Every address is validated here, at load time, so a typo in
// the book fails immediately instead of surfacing as a confusing host error
// later.
func NewAddressBook(entries map[string]string) (*AddressBook, error) {
	b := &AddressBook{byName: make(map[string]string, len(entries))}
	for rawName, address := range entries {
		name, err := normalizeName(rawName)
		if err != nil {
			return nil, err
		}
		if _, dup := b.byName[name]; dup {
			return nil, fmt.Errorf("address book: duplicate name %q", name)
		}
		if !strkey.IsValidEd25519PublicKey(address) && !strkey.IsValidContractAddress(address) {
			return nil, fmt.Errorf(
				"address book: %q maps to %q, which is not a valid account (G...) or contract (C...) address",
				name, address)
		}
		b.byName[name] = address
	}
	return b, nil
}

// normalizeName strips the optional leading "@" and rejects names that would be
// ambiguous inside a "type:value" argument.
func normalizeName(raw string) (string, error) {
	name := strings.TrimPrefix(strings.TrimSpace(raw), "@")
	if name == "" {
		return "", fmt.Errorf("address book: a name is required")
	}
	if strings.ContainsAny(name, "@: \t\r\n") {
		return "", fmt.Errorf("address book: name %q must not contain '@', ':' or whitespace", name)
	}
	return name, nil
}

// Names lists the known names, sorted, for error messages and help text.
func (b *AddressBook) Names() []string {
	if b == nil {
		return nil
	}
	names := make([]string, 0, len(b.byName))
	for name := range b.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Resolve rewrites an argument that names a book entry.
//
// It accepts "addr:@name", "address:@name" and a bare "@name". Anything else
// is returned unchanged, so ordinary arguments pass through untouched. An
// unknown name is an error that lists the names that do exist.
func (b *AddressBook) Resolve(spec string) (string, error) {
	if b == nil {
		return spec, nil
	}

	prefix := ""
	value := strings.TrimSpace(spec)
	if p, literal, ok := splitSpec(value); ok {
		if p == "addr" || p == "address" {
			prefix = p
			value = strings.TrimSpace(literal)
		}
	}
	if !strings.HasPrefix(value, "@") {
		return spec, nil
	}

	name := strings.TrimPrefix(value, "@")
	address, ok := b.byName[name]
	if !ok {
		if prefix == "" && len(b.byName) == 0 {
			// With no book, a bare "@..." is still an ordinary literal. Only
			// addr:@name promises a lookup, and only that form should fail
			// with the address-book diagnostic.
			return spec, nil
		}
		if len(b.byName) == 0 {
			return "", fmt.Errorf("unknown address name %q: the address book is empty", "@"+name)
		}
		return "", fmt.Errorf("unknown address name %q (known: %s)", "@"+name, strings.Join(b.Names(), ", "))
	}
	if prefix == "" {
		// A bare name carries no type. Be explicit in the rewritten spec so
		// encoded_args is unambiguous.
		prefix = "addr"
	}
	return prefix + ":" + address, nil
}

// ResolveAll resolves every argument, reporting whether any changed. The error
// names the argument index, matching EncodeAll's diagnostics.
func (b *AddressBook) ResolveAll(specs []string) ([]string, bool, error) {
	if b == nil || len(specs) == 0 {
		return specs, false, nil
	}

	out := make([]string, len(specs))
	changed := false
	for i, spec := range specs {
		resolved, err := b.Resolve(spec)
		if err != nil {
			return nil, false, fmt.Errorf("arg %d: %w", i, err)
		}
		out[i] = resolved
		if resolved != spec {
			changed = true
		}
	}
	return out, changed, nil
}
