package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/scval"
)

// CheckRequest describes a combined health check.
type CheckRequest struct {
	// ContractID is the strkey contract address ("C...").
	ContractID string `json:"contract_id"`
	// Function, when set, names a read-only function to simulate as a
	// liveness probe.
	//
	// It is optional on purpose. SoroProbe cannot discover a contract's
	// exported functions from the ledger, and inventing a name would produce a
	// misleading failure, so the simulation step is reported as skipped rather
	// than guessed at.
	Function string `json:"function,omitempty"`
	// Args are argument specifications for Function.
	Args []string `json:"args,omitempty"`
	// Expect is an optional argument specification for the expected return value.
	Expect string `json:"expect,omitempty"`
	// DataKeys are optional data entries to include in the TTL assessment.
	DataKeys []string `json:"data_keys,omitempty"`
	// DataDurability selects the durability DataKeys are read at.
	DataDurability health.Durability `json:"data_durability,omitempty"`
}

// CheckOutcome is the result of one individual check.
type CheckOutcome string

const (
	// OutcomePass means the check succeeded.
	OutcomePass CheckOutcome = "pass"
	// OutcomeWarn means the check succeeded but something needs attention.
	OutcomeWarn CheckOutcome = "warn"
	// OutcomeFail means the check failed.
	OutcomeFail CheckOutcome = "fail"
	// OutcomeSkip means the check was not run.
	OutcomeSkip CheckOutcome = "skip"
)

// Check is one named assertion about a contract.
type Check struct {
	Name    string       `json:"name"`
	Outcome CheckOutcome `json:"outcome"`
	Detail  string       `json:"detail"`
}

// CheckResult aggregates the individual checks.
type CheckResult struct {
	ContractID string `json:"contract_id"`
	// OK is false when any check failed. The CLI exits non-zero on false.
	OK bool `json:"ok"`
	// Status is the worst state-health status observed.
	Status health.Status `json:"status"`
	// Checks lists each assertion in the order it was evaluated.
	Checks []Check `json:"checks"`
	// Inspect is the full state-health report the checks were derived from.
	Inspect *InspectResult `json:"inspect,omitempty"`
	// Simulate is the invocation result, when a function was given.
	Simulate *SimulateResult `json:"simulate,omitempty"`
	// LatestLedger is the ledger the assessment was made against.
	LatestLedger uint32 `json:"latest_ledger"`
}

// Check runs the combined health check: is the contract deployed, are its
// instance, code and named data entries live and not near expiration, and — if
// a function was given — does a read-only call simulate successfully.
func (p *Prober) Check(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	inspect, err := p.Inspect(ctx, InspectRequest{
		ContractID:     req.ContractID,
		DataKeys:       req.DataKeys,
		DataDurability: req.DataDurability,
	})
	if err != nil {
		return nil, err
	}

	result := &CheckResult{
		ContractID:   req.ContractID,
		Inspect:      inspect,
		Status:       inspect.Status,
		LatestLedger: inspect.LatestLedger,
	}

	// 1. Deployment.
	if !inspect.Deployed {
		result.Checks = append(result.Checks, Check{
			Name:    "deployed",
			Outcome: OutcomeFail,
			Detail:  "no contract instance entry found on this network",
		})
		result.OK = false
		return result, nil
	}
	result.Checks = append(result.Checks, Check{
		Name:    "deployed",
		Outcome: OutcomePass,
		Detail:  fmt.Sprintf("instance found, executable is %s", inspect.Executable),
	})

	// 2. Instance TTL.
	result.Checks = append(result.Checks, ttlCheck("instance_ttl", inspect.Instance))

	// 3. Code TTL, when the contract has Wasm code.
	if inspect.Code != nil {
		result.Checks = append(result.Checks, ttlCheck("code_ttl", *inspect.Code))
	} else {
		result.Checks = append(result.Checks, Check{
			Name:    "code_ttl",
			Outcome: OutcomeSkip,
			Detail:  "stellar asset contracts are built into the host and have no code entry",
		})
	}

	// 4. Any explicitly named data entries.
	for _, entry := range inspect.Data {
		result.Checks = append(result.Checks, ttlCheck("data_ttl:"+entry.Key, entry))
	}

	// 5. Read-only simulation, when a function was named.
	simCheck, err := p.simulationCheck(ctx, req, result)
	if err != nil {
		return nil, err
	}
	result.Checks = append(result.Checks, simCheck)

	// 6. Expected return value, when an expectation was given.
	if req.Expect != "" {
		result.Checks = append(result.Checks, p.expectCheck(req, result))
	}

	result.OK = true
	for _, c := range result.Checks {
		if c.Outcome == OutcomeFail {
			result.OK = false
			break
		}
	}
	return result, nil
}

func (p *Prober) simulationCheck(ctx context.Context, req CheckRequest, result *CheckResult) (Check, error) {
	if req.Function == "" {
		return Check{
			Name:    "simulate",
			Outcome: OutcomeSkip,
			Detail:  "no function given; pass one to probe a read-only call",
		}, nil
	}

	sim, err := p.Simulate(ctx, SimulateRequest{
		ContractID: req.ContractID,
		Function:   req.Function,
		Args:       req.Args,
	})
	if err != nil {
		return Check{}, err
	}
	result.Simulate = sim

	if !sim.Success {
		return Check{
			Name:    "simulate",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("calling %s failed: %s", req.Function, sim.Error),
		}, nil
	}
	return Check{
		Name:    "simulate",
		Outcome: OutcomePass,
		Detail: fmt.Sprintf("calling %s succeeded, %d instructions, min resource fee %d stroops",
			req.Function, sim.Cost.Instructions, sim.Cost.MinResourceFee),
	}, nil
}

// ttlCheck converts a TTL assessment into a pass/warn/fail outcome. A warning
// threshold breach is deliberately not a failure: it is a signal to extend the
// entry's TTL, not a reason to fail a CI pipeline.
func ttlCheck(name string, entry EntryReport) Check {
	outcome := OutcomeFail
	switch entry.Status {
	case health.StatusHealthy, health.StatusUnknown:
		outcome = OutcomePass
	case health.StatusWarning:
		outcome = OutcomeWarn
	}
	return Check{Name: name, Outcome: outcome, Detail: entry.Summary}
}

func (p *Prober) expectCheck(req CheckRequest, result *CheckResult) Check {
	if req.Function == "" {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  "an expected value was given but no function was specified",
		}
	}
	if result.Simulate == nil || !result.Simulate.Success {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  "cannot check return value because simulation failed or did not run",
		}
	}

	expectedVal, err := p.codec.Encode(req.Expect)
	if err != nil {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("invalid expected value %q: %v", req.Expect, err),
		}
	}

	var actualVal xdr.ScVal
	if result.Simulate.ReturnValueXDR != "" {
		if err := xdr.SafeUnmarshalBase64(result.Simulate.ReturnValueXDR, &actualVal); err != nil {
			return Check{
				Name:    "expect",
				Outcome: OutcomeFail,
				Detail:  fmt.Sprintf("decode actual return value: %v", err),
			}
		}
	} else {
		actualVal = xdr.ScVal{Type: xdr.ScValTypeScvVoid}
	}

	match, typeMismatch := compareScVals(expectedVal, actualVal)

	expDecoded, _ := p.codec.Decode(expectedVal)
	actDecoded := result.Simulate.ReturnValue
	expDisp := formatVal(expDecoded)
	actDisp := formatVal(actDecoded)

	if match {
		return Check{
			Name:    "expect",
			Outcome: OutcomePass,
			Detail:  fmt.Sprintf("expected %s, got %s", expDisp, actDisp),
		}
	}

	if typeMismatch {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("type mismatch: expected %s, got %s", expDisp, actDisp),
		}
	}

	return Check{
		Name:    "expect",
		Outcome: OutcomeFail,
		Detail:  fmt.Sprintf("expected %s, got %s", expDisp, actDisp),
	}
}

func compareScVals(expected, actual xdr.ScVal) (match bool, typeMismatch bool) {
	expInt, expIsInt := scval.ToBigInt(expected)
	actInt, actIsInt := scval.ToBigInt(actual)

	if expIsInt || actIsInt {
		if expIsInt && actIsInt {
			return expInt.Cmp(actInt) == 0, false
		}
		return false, true
	}

	if expected.Type != actual.Type {
		return false, true
	}

	expBytes, err1 := expected.MarshalBinary()
	actBytes, err2 := actual.MarshalBinary()
	if err1 == nil && err2 == nil {
		return bytes.Equal(expBytes, actBytes), false
	}

	return reflect.DeepEqual(expected, actual), false
}

func formatVal(v any) string {
	if v == nil {
		return "(void)"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
