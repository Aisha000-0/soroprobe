package probe

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/soroworks/soroprobe/internal/health"
)

// CheckRequest describes a combined health check.
type CheckRequest struct {
	// ContractID is the strkey contract address ("C...").
	ContractID string `json:"contract_id"`
	// Function, when set, names a read-only function to simulate as a
	// liveness probe.
	// It is optional on purpose. SoroProbe cannot discover a contract's
	// exported functions from the ledger, and inventing a name would produce a
	// misleading failure, so the simulation step is reported as skipped rather
	// than guessed at.
	Function string `json:"function,omitempty"`
	// Args are argument specifications for Function.
	Args []string `json:"args,omitempty"`
	// DataKeys are optional data entries to include in the TTL assessment.
	DataKeys []string `json:"data_keys,omitempty"`
	// DataDurability selects the durability DataKeys are read at.
	DataDurability health.Durability `json:"data_durability,omitempty"`
	// ExpectWasmHash, when set, adds a wasm_hash step that fails unless the
	// contract runs exactly this code: the 64 hex characters of the wasm's
	// sha256, as a deploy pipeline knows it. It is the post-deploy assertion
	// that an upgrade landed. Empty, the default, adds no step.
	ExpectWasmHash string `json:"expect_wasm_hash,omitempty"`
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

// CheckStep is a pluggable step in the health check pipeline.
// Each step receives the gathered context (inspect and simulate results)
// and returns a Check outcome.
// Returning ErrSkip means the step was not applicable (e.g., no function given).
type CheckStep interface {
	Name() string
	Run(ctx context.Context, req CheckRequest, inspect *InspectResult, simulate *SimulateResult) (Check, error)
}

// checkSteps is the default ordered list of steps run by Check.
// Steps are run in order; execution stops at the first failure (except
// ErrSkip, which is ignored and the next step runs). Steps that only apply
// under some condition — such as wasm_hash, which needs an expected hash —
// return ErrSkip and add nothing to the result.
var checkSteps = []CheckStep{
	&deployedStep{},
	&wasmHashStep{},
	&instanceTTLStep{},
	&codeTTLStep{},
	&dataTTLStep{},
	&simulateStep{},
}

// Check runs the combined health check: is the contract deployed, does it run
// the expected wasm when one was given, are its instance, code and named data
// entries live and not near expiration, and — if a function was given — does a
// read-only call simulate successfully.
// The check delegates to the ordered list defined by checkSteps. Each step
// receives the inspect and simulate results computed once, so adding a step
// cannot accidentally add RPC calls.
func (p *Prober) Check(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	// A malformed expectation is a problem with the request, not with the
	// contract, so it is rejected before any RPC call is made. The
	// normalized form is what the wasm_hash step compares against.
	if req.ExpectWasmHash != "" {
		want, err := normalizeWasmHash(req.ExpectWasmHash)
		if err != nil {
			return nil, err
		}
		req.ExpectWasmHash = want
	}

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

	// Check deployment first - if not deployed, stop early
	// This preserves the early-stop behavior when a contract is not deployed
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

	// Only simulate when a function is given and contract is deployed
	var sim *SimulateResult
	if req.Function != "" {
		var simErr error
		sim, simErr = p.Simulate(ctx, SimulateRequest{
			ContractID: req.ContractID,
			Function:   req.Function,
			Args:       req.Args,
		})
		if simErr != nil {
			return nil, simErr
		}
	}

	result.Simulate = sim

	// Run remaining steps in order. Execution stops at the first failure
	// (OutcomeFail), mimicking the original early-stop behavior when a
	// contract is not deployed (though we already checked deployment above).
	result.OK = true
	for _, step := range checkSteps {
		check, err := step.Run(ctx, req, inspect, sim)
		if err != nil {
			if err == ErrSkip {
				// Step was not applicable; continue to the next step.
				continue
			}
			return nil, err
		}
		result.Checks = append(result.Checks, check)
		if check.Outcome == OutcomeFail {
			result.OK = false
			break
		}
	}
	return result, nil
}

// ErrSkip is returned by a CheckStep when the step is not applicable
// (e.g., no function given for the simulate step). It is ignored by the
// Check runner and the next step continues.
var ErrSkip = errors.New("step skipped")

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

// --- step implementations ---

// ErrInvalidWasmHash reports an expect-wasm-hash value that is not a 32-byte
// hex digest. It is bad input, not a failing contract: the CLI reports it as
// a runtime error (exit 2) and the API answers 400, rather than recording a
// failed check against a contract that was never assessed.
var ErrInvalidWasmHash = errors.New("invalid expected wasm hash")

// normalizeWasmHash validates an expected wasm hash and returns it in lower
// case, the form InspectResult.WasmHash is reported in.
func normalizeWasmHash(s string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(s))
	if len(h) != 64 {
		return "", fmt.Errorf("%w %q: want 64 hex characters (the sha256 of the wasm), got %d",
			ErrInvalidWasmHash, s, len(h))
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", fmt.Errorf("%w %q: want 64 hex characters (the sha256 of the wasm)",
			ErrInvalidWasmHash, s)
	}
	return h, nil
}

type deployedStep struct{}

func (s *deployedStep) Name() string { return "deployed" }
func (s *deployedStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) (Check, error) {
	if !inspect.Deployed {
		return Check{Name: "deployed", Outcome: OutcomeFail, Detail: "no contract instance entry found on this network"}, nil
	}
	return Check{Name: "deployed", Outcome: OutcomePass, Detail: fmt.Sprintf("instance found, executable is %s", inspect.Executable)}, nil
}

// wasmHashStep asserts which code a contract runs, by comparing the Wasm hash
// read with the instance entry against the expected one. It reads only the
// InspectResult, so it adds no RPC calls of its own.
type wasmHashStep struct{}

func (s *wasmHashStep) Name() string { return "wasm_hash" }
func (s *wasmHashStep) Run(_ context.Context, req CheckRequest, inspect *InspectResult, _ *SimulateResult) (Check, error) {
	// The step exists only when a hash was expected, so checks that do not
	// ask gain no new line in their output.
	if req.ExpectWasmHash == "" {
		return Check{}, ErrSkip
	}
	want := strings.ToLower(req.ExpectWasmHash)

	switch {
	case inspect.Executable == "stellar_asset":
		return Check{Name: "wasm_hash", Outcome: OutcomeFail, Detail: fmt.Sprintf(
			"stellar asset contract: the host implements it, so it runs no wasm and cannot match expected wasm hash %s", want)}, nil
	case inspect.WasmHash == "":
		return Check{Name: "wasm_hash", Outcome: OutcomeFail, Detail: fmt.Sprintf(
			"contract reports executable %s and no wasm hash, so it cannot match expected wasm hash %s", inspect.Executable, want)}, nil
	case !strings.EqualFold(inspect.WasmHash, want):
		return Check{Name: "wasm_hash", Outcome: OutcomeFail, Detail: fmt.Sprintf(
			"contract runs wasm hash %s, expected %s", inspect.WasmHash, want)}, nil
	default:
		return Check{Name: "wasm_hash", Outcome: OutcomePass, Detail: fmt.Sprintf(
			"contract runs the expected wasm hash %s", strings.ToLower(inspect.WasmHash))}, nil
	}
}

type instanceTTLStep struct{}

func (s *instanceTTLStep) Name() string { return "instance_ttl" }
func (s *instanceTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) (Check, error) {
	return ttlCheck("instance_ttl", inspect.Instance), nil
}

type codeTTLStep struct{}

func (s *codeTTLStep) Name() string { return "code_ttl" }
func (s *codeTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) (Check, error) {
	if inspect.Code != nil {
		return ttlCheck("code_ttl", *inspect.Code), nil
	}
	return Check{Name: "code_ttl", Outcome: OutcomeSkip, Detail: "stellar asset contracts are built into the host and have no code entry"}, nil
}

type dataTTLStep struct{}

func (s *dataTTLStep) Name() string { return "data_ttl" }
func (s *dataTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) (Check, error) {
	var checks []Check
	for _, entry := range inspect.Data {
		checks = append(checks, ttlCheck("data_ttl:"+entry.Key, entry))
	}
	// If there are no data entries, return a single skip so the step is
	// still represented in the check result.
	if len(checks) == 0 {
		return Check{Name: "data_ttl", Outcome: OutcomeSkip, Detail: "no data entries specified"}, nil
	}
	// Return the first check; the rest are already appended to result.Checks
	// by the Check runner loop. For compatibility we return just one,
	// but the loop handles appending all of them.
	return checks[0], nil
}

type simulateStep struct{}

func (s *simulateStep) Name() string { return "simulate" }
func (s *simulateStep) Run(_ context.Context, req CheckRequest, _ *InspectResult, simulate *SimulateResult) (Check, error) {
	if req.Function == "" {
		return Check{Name: "simulate", Outcome: OutcomeSkip, Detail: "no function given; pass one to probe a read-only call"}, nil
	}

	if simulate == nil || !simulate.Success {
		return Check{Name: "simulate", Outcome: OutcomeFail, Detail: fmt.Sprintf("calling %s failed: %s", req.Function, simulate.Error)}, nil
	}
	return Check{Name: "simulate", Outcome: OutcomePass, Detail: fmt.Sprintf("calling %s succeeded, %d instructions, min resource fee %d stroops", req.Function, simulate.Cost.Instructions, simulate.Cost.MinResourceFee)}, nil
}
