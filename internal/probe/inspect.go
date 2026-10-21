package probe

import (
	"context"
	"encoding/hex"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// InspectRequest describes a state-health read.
type InspectRequest struct {
	// ContractID is the strkey contract address ("C...").
	ContractID string `json:"contract_id"`
	// DataKeys are optional argument specifications, in the scval package's
	// "type:value" form, naming contract data entries to inspect alongside the
	// instance and code.
	//
	// Stellar RPC reads ledger entries by key and offers no way to enumerate a
	// contract's data entries, so callers that want specific entries checked
	// must name them. Enumerating all of them requires an indexer, which is out
	// of scope for a stateless tool.
	DataKeys []string `json:"data_keys,omitempty"`
	// DataDurability selects which durability the DataKeys are read at.
	// Defaults to persistent.
	DataDurability health.Durability `json:"data_durability,omitempty"`
}

// EntryReport is the health of a single ledger entry.
type EntryReport struct {
	// Kind is "instance", "code" or "data".
	Kind string `json:"kind"`
	// Key describes which entry this is, for data entries.
	Key string `json:"key,omitempty"`
	// TTL is the interpreted expiration state.
	health.TTL
	// Summary is a human-readable one-liner.
	Summary string `json:"summary"`
}

// InspectResult reports a contract's on-ledger state health.
type InspectResult struct {
	ContractID string `json:"contract_id"`
	// Deployed is true when the contract's instance entry exists.
	Deployed bool `json:"deployed"`
	// Executable is "wasm" or "stellar_asset".
	Executable string `json:"executable,omitempty"`
	// WasmHash is the hex code hash, for Wasm contracts.
	WasmHash string `json:"wasm_hash,omitempty"`

	// Instance is the contract instance entry's health.
	Instance EntryReport `json:"instance"`
	// Code is the Wasm code entry's health. Nil for Stellar Asset Contracts,
	// which are built into the host and have no code entry.
	Code *EntryReport `json:"code,omitempty"`
	// Data holds any explicitly requested data entries.
	Data []EntryReport `json:"data,omitempty"`

	// InstanceStorage is the decoded instance storage map. These values live
	// and die with the instance entry: they share its single TTL.
	InstanceStorage any `json:"instance_storage,omitempty"`

	// Status is the worst status across all inspected entries.
	Status health.Status `json:"status"`
	// LatestLedger is the ledger the assessment was made against.
	LatestLedger uint32 `json:"latest_ledger"`
}

// Inspect reads a contract's instance, code and any named data entries, and
// reports how close each is to expiration.
//
// When the Prober has an inspect cache, a repeated request within its window is
// served from it. A cached report is still honest: LatestLedger records the
// ledger it was computed at, and simulations are never cached.
func (p *Prober) Inspect(ctx context.Context, req InspectRequest) (*InspectResult, error) {
	if p.inspectCache == nil {
		return p.inspectUncached(ctx, req)
	}

	key := inspectCacheKey(req)
	if result, ok := p.inspectCache.get(key); ok {
		return result, nil
	}

	result, err := p.inspectUncached(ctx, req)
	if err != nil {
		return nil, err
	}
	p.inspectCache.put(key, result)
	return result, nil
}

// inspectUncached is Inspect without the cache.
func (p *Prober) inspectUncached(ctx context.Context, req InspectRequest) (*InspectResult, error) {
	contract, err := stellar.ParseContractID(req.ContractID)
	if err != nil {
		return nil, err
	}

	durability := req.DataDurability
	if durability == "" {
		durability = health.DurabilityPersistent
	}

	// Round one: the instance entry, plus any requested data entries. The code
	// entry's key is not known until the instance reveals its Wasm hash.
	instanceKey, err := stellar.EncodeKey(stellar.InstanceKey(contract))
	if err != nil {
		return nil, err
	}
	keys := []string{instanceKey}

	dataKeys, err := p.encodeDataKeys(contract, req.DataKeys, durability)
	if err != nil {
		return nil, err
	}
	for _, dk := range dataKeys {
		keys = append(keys, dk.encoded)
	}

	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys})
	if err != nil {
		return nil, err
	}
	found := indexEntries(resp.Entries)

	result := &InspectResult{
		ContractID:   req.ContractID,
		LatestLedger: resp.LatestLedger,
	}

	instance, ok := found[instanceKey]
	if !ok {
		result.Instance = EntryReport{
			Kind:    "instance",
			TTL:     health.Absent(resp.LatestLedger),
			Summary: "contract is not deployed on this network",
		}
		result.Status = health.StatusMissing
		return result, nil
	}

	result.Deployed = true
	result.Instance = EntryReport{
		Kind: "instance",
		TTL:  health.Evaluate(instance.LiveUntilLedgerSeq, resp.LatestLedger, health.DurabilityPersistent, p.thresholds),
	}
	result.Instance.Summary = result.Instance.Describe()

	wasmHash, err := p.readInstanceEntry(instance, result)
	if err != nil {
		return nil, err
	}

	for _, dk := range dataKeys {
		report := EntryReport{Kind: "data", Key: dk.spec}
		if entry, ok := found[dk.encoded]; ok {
			report.TTL = health.Evaluate(entry.LiveUntilLedgerSeq, resp.LatestLedger, durability, p.thresholds)
		} else {
			report.TTL = health.Absent(resp.LatestLedger)
			report.Durability = durability
		}
		report.Summary = report.Describe()
		result.Data = append(result.Data, report)
	}

	// Round two: the code entry, now that the Wasm hash is known.
	if wasmHash != nil {
		codeReport, err := p.inspectCode(ctx, *wasmHash)
		if err != nil {
			return nil, err
		}
		result.Code = codeReport
	}

	result.Status = result.worstStatus()
	return result, nil
}

// readInstanceEntry decodes the instance entry, filling in the executable,
// Wasm hash and instance storage. It returns the Wasm hash when there is one.
func (p *Prober) readInstanceEntry(entry protocol.LedgerEntryResult, result *InspectResult) (*xdr.Hash, error) {
	data, err := stellar.DecodeEntryData(entry.DataXDR)
	if err != nil {
		return nil, err
	}
	if data.ContractData == nil {
		return nil, fmt.Errorf("instance entry is not contract data (got %s)", data.Type)
	}

	instance, ok := data.ContractData.Val.GetInstance()
	if !ok {
		return nil, fmt.Errorf("instance entry does not hold a contract instance")
	}

	var wasmHash *xdr.Hash
	switch instance.Executable.Type {
	case xdr.ContractExecutableTypeContractExecutableWasm:
		result.Executable = "wasm"
		if instance.Executable.WasmHash != nil {
			result.WasmHash = hex.EncodeToString(instance.Executable.WasmHash[:])
			hash := *instance.Executable.WasmHash
			wasmHash = &hash
		}
	case xdr.ContractExecutableTypeContractExecutableStellarAsset:
		// Stellar Asset Contracts are implemented by the host itself, so there
		// is no Wasm code entry to look up or to expire.
		result.Executable = "stellar_asset"
	default:
		result.Executable = instance.Executable.Type.String()
	}

	if instance.Storage != nil {
		storage, err := p.decodeStorage(instance.Storage)
		if err != nil {
			return nil, err
		}
		result.InstanceStorage = storage
	}
	return wasmHash, nil
}

func (p *Prober) decodeStorage(storage *xdr.ScMap) (any, error) {
	decoded, err := p.codec.Decode(xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &storage})
	if err != nil {
		return nil, fmt.Errorf("decode instance storage: %w", err)
	}
	return decoded, nil
}

func (p *Prober) inspectCode(ctx context.Context, hash xdr.Hash) (*EntryReport, error) {
	key, err := stellar.EncodeKey(stellar.CodeKey(hash))
	if err != nil {
		return nil, err
	}

	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{key}})
	if err != nil {
		return nil, err
	}

	report := &EntryReport{Kind: "code"}
	if len(resp.Entries) == 0 {
		report.TTL = health.Absent(resp.LatestLedger)
		report.Summary = "wasm code entry not found; it may have been archived"
		return report, nil
	}

	report.TTL = health.Evaluate(resp.Entries[0].LiveUntilLedgerSeq, resp.LatestLedger, health.DurabilityPersistent, p.thresholds)
	report.Summary = report.Describe()
	return report, nil
}

type encodedDataKey struct {
	spec    string
	encoded string
}

func (p *Prober) encodeDataKeys(contract xdr.ScAddress, specs []string, durability health.Durability) ([]encodedDataKey, error) {
	out := make([]encodedDataKey, 0, len(specs))
	for _, spec := range specs {
		val, err := p.codec.Encode(spec)
		if err != nil {
			return nil, err
		}
		xdrDurability := xdr.ContractDataDurabilityPersistent
		if durability == health.DurabilityTemporary {
			xdrDurability = xdr.ContractDataDurabilityTemporary
		}
		encoded, err := stellar.EncodeKey(stellar.DataKey(contract, val, xdrDurability))
		if err != nil {
			return nil, err
		}
		out = append(out, encodedDataKey{spec: spec, encoded: encoded})
	}
	return out, nil
}

// indexEntries maps results back to the keys that were requested. RPC omits
// entries that do not exist, so absence from this map means "not on ledger".
func indexEntries(entries []protocol.LedgerEntryResult) map[string]protocol.LedgerEntryResult {
	out := make(map[string]protocol.LedgerEntryResult, len(entries))
	for _, e := range entries {
		out[e.KeyXDR] = e
	}
	return out
}

func (r *InspectResult) worstStatus() health.Status {
	statuses := []health.Status{r.Instance.Status}
	if r.Code != nil {
		statuses = append(statuses, r.Code.Status)
	}
	for _, d := range r.Data {
		statuses = append(statuses, d.Status)
	}
	return health.Worst(statuses...)
}

// ensure the codec interface is what inspect relies on
var _ scval.Codec = (*scval.Registry)(nil)
