package probe

import (
	"context"
	"errors"
	"fmt"

	"github.com/soroworks/soroprobe/internal/health"
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

// CheckStep is one pluggable step in the health check pipeline.
//
// Run receives the request and the state gathered once by Check — the inspect
// report and, when one was needed, the simulation — and returns the checks it
// contributes, in the order they should be reported. A step that has nothing to
// say returns no checks; a step that does not apply usually returns a single
// OutcomeSkip check so the report still names it.
//
// Returning errStop ends the pipeline after the step's checks are recorded. Any
// other error aborts Check.
type CheckStep interface {
	Name() string
	Run(ctx context.Context, req CheckRequest, inspect *InspectResult, simulate *SimulateResult) ([]Check, error)
}

// errStop ends the pipeline once a step's checks are recorded. Only the
// deployment step uses it: with no instance there is nothing further worth
// probing, which is the one early stop the checks have always had. Other
// failures are still reported in full.
var errStop = errors.New("stop checking")

// checkSteps is the ordered list Check runs. To add a check, implement
// CheckStep and append it here at the position its result should appear; a step
// reads the inspect and simulation state Check gathered once, so it cannot add
// an RPC call by itself.
var checkSteps = []CheckStep{
	&deployedStep{},
	&instanceTTLStep{},
	&codeTTLStep{},
	&dataTTLStep{},
	&simulateStep{},
}

// Check runs the combined health check: is the contract deployed, are its
// instance, code and named data entries live and not near expiration, and — if
// a function was given — does a read-only call simulate successfully.
//
// The work is done by the ordered steps in checkSteps. Check gathers the
// inspect report and the simulation once and hands both to every step, so
// adding a step cannot accidentally add an RPC call.
func (p *Prober) Check(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	inspect, err := p.Inspect(ctx, InspectRequest{
		ContractID:     req.ContractID,
		DataKeys:       req.DataKeys,
		DataDurability: req.DataDurability,
	})
	if err != nil {
		return nil, err
	}

	// Gather the simulation once, and only when a step can use it: an
	// undeployed contract fails the first step, and a check without a function
	// never reports a simulation, so in both cases calling Simulate would spend
	// an RPC call whose result no step reads.
	var sim *SimulateResult
	if inspect.Deployed && req.Function != "" {
		sim, err = p.Simulate(ctx, SimulateRequest{
			ContractID: req.ContractID,
			Function:   req.Function,
			Args:       req.Args,
		})
		if err != nil {
			return nil, err
		}
	}

	result := &CheckResult{
		ContractID:   req.ContractID,
		Inspect:      inspect,
		Status:       inspect.Status,
		Simulate:     sim,
		LatestLedger: inspect.LatestLedger,
	}

	for _, step := range checkSteps {
		checks, err := step.Run(ctx, req, inspect, sim)
		result.Checks = append(result.Checks, checks...)
		if err != nil {
			if errors.Is(err, errStop) {
				break
			}
			return nil, err
		}
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

// deployedStep reports whether the contract has an instance on this network.
// It is the pipeline's precondition, so a missing instance stops the run.
type deployedStep struct{}

func (s *deployedStep) Name() string { return "deployed" }

func (s *deployedStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if !inspect.Deployed {
		return []Check{{
			Name:    "deployed",
			Outcome: OutcomeFail,
			Detail:  "no contract instance entry found on this network",
		}}, errStop
	}
	return []Check{{
		Name:    "deployed",
		Outcome: OutcomePass,
		Detail:  fmt.Sprintf("instance found, executable is %s", inspect.Executable),
	}}, nil
}

// instanceTTLStep reports the contract instance's expiration health.
type instanceTTLStep struct{}

func (s *instanceTTLStep) Name() string { return "instance_ttl" }

func (s *instanceTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	return []Check{ttlCheck("instance_ttl", inspect.Instance)}, nil
}

// codeTTLStep reports the Wasm code entry's expiration health. Stellar Asset
// Contracts have no code entry, so the step is reported as skipped for them.
type codeTTLStep struct{}

func (s *codeTTLStep) Name() string { return "code_ttl" }

func (s *codeTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if inspect.Code != nil {
		return []Check{ttlCheck("code_ttl", *inspect.Code)}, nil
	}
	return []Check{{
		Name:    "code_ttl",
		Outcome: OutcomeSkip,
		Detail:  "stellar asset contracts are built into the host and have no code entry",
	}}, nil
}

// dataTTLStep reports one check per named data entry. With no keys it
// contributes nothing, matching the report's previous shape.
type dataTTLStep struct{}

func (s *dataTTLStep) Name() string { return "data_ttl" }

func (s *dataTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	checks := make([]Check, 0, len(inspect.Data))
	for _, entry := range inspect.Data {
		checks = append(checks, ttlCheck("data_ttl:"+entry.Key, entry))
	}
	return checks, nil
}

// simulateStep reports the read-only call's outcome. Without a function there
// is nothing to assert, so it is reported as skipped.
type simulateStep struct{}

func (s *simulateStep) Name() string { return "simulate" }

func (s *simulateStep) Run(_ context.Context, req CheckRequest, _ *InspectResult, simulate *SimulateResult) ([]Check, error) {
	if req.Function == "" {
		return []Check{{
			Name:    "simulate",
			Outcome: OutcomeSkip,
			Detail:  "no function given; pass one to probe a read-only call",
		}}, nil
	}
	if simulate == nil {
		return nil, errors.New("simulate step has no simulation result")
	}
	if !simulate.Success {
		return []Check{{
			Name:    "simulate",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("calling %s failed: %s", req.Function, simulate.Error),
		}}, nil
	}
	return []Check{{
		Name:    "simulate",
		Outcome: OutcomePass,
		Detail: fmt.Sprintf("calling %s succeeded, %d instructions, min resource fee %d stroops",
			req.Function, simulate.Cost.Instructions, simulate.Cost.MinResourceFee),
	}}, nil
}
