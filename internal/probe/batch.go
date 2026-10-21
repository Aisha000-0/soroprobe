package probe

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/soroworks/soroprobe/internal/health"
)

// MaxBatchChecks bounds a batch, so one API request cannot queue an
// unbounded amount of RPC work.
const MaxBatchChecks = 100

// ErrInvalidBatch reports a malformed batch: empty, too large, or with
// duplicate or invalid entries. Nothing is checked when it is returned.
var ErrInvalidBatch = errors.New("invalid check batch")

// NamedCheck is one entry in a batch: a CheckRequest with a name that
// identifies it in the results. The name defaults to the contract ID.
type NamedCheck struct {
	Name string `json:"name,omitempty"`
	// Timeout bounds this one check, as a Go duration string ("45s"). Empty
	// means no per-check limit beyond the context the caller passes; this is
	// what keeps one slow contract from consuming a whole batch's budget.
	Timeout string `json:"timeout,omitempty"`
	CheckRequest
}

// BatchRequest is a list of checks to run together.
type BatchRequest struct {
	Checks []NamedCheck `json:"checks"`
}

// BatchItem is the outcome of one check in a batch. Exactly one of Result
// and Error is set: Error means the check could not be run at all.
type BatchItem struct {
	Name       string       `json:"name"`
	ContractID string       `json:"contract_id"`
	Result     *CheckResult `json:"result,omitempty"`
	Error      string       `json:"error,omitempty"`
}

// BatchResult aggregates a batch.
type BatchResult struct {
	// OK is true only when every check ran and passed.
	OK bool `json:"ok"`
	// Errored is true when at least one check could not be run. It is kept
	// separate from OK so a pipeline can still tell "a contract is
	// unhealthy" from "the tool could not tell".
	Errored bool        `json:"errored"`
	Items   []BatchItem `json:"items"`
}

// CheckBatch runs each check in order and reports them together.
//
// Checks run sequentially: a batch is usually pointed at a public RPC
// endpoint, and firing every check at once would trade a few seconds for
// rate-limit failures. One check failing to run does not stop the others;
// its error is recorded and the batch moves on. A check that outlives its
// own Timeout is reported as one that could not run, and the batch continues;
// the limit is enforced by CheckBatch itself (see runBounded), so it holds
// even for an RPC that is already in flight and cannot be cancelled. Only a
// malformed batch or a cancelled context returns a Go error.
func (p *Prober) CheckBatch(ctx context.Context, req BatchRequest) (*BatchResult, error) {
	if err := validateBatch(req); err != nil {
		return nil, err
	}

	out := &BatchResult{OK: true, Items: make([]BatchItem, 0, len(req.Checks))}
	for _, c := range req.Checks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Already validated, so the error cannot fire here.
		timeout, _ := c.checkTimeout()
		checkCtx, cancel := withCheckTimeout(ctx, timeout)

		item := BatchItem{Name: checkName(c), ContractID: c.ContractID}
		result, err := runBounded(checkCtx, p, c.CheckRequest)
		cancel()
		switch {
		case err != nil:
			// A cancelled caller aborts the batch; a check that merely ran
			// out of its own time is one result among many.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Only when a per-check limit was set does a deadline mean "this
			// check timed out"; with none, the transport's own per-request
			// timeout is what fires, and it stays an ordinary error.
			if timeout > 0 && errors.Is(err, context.DeadlineExceeded) {
				item.Error = fmt.Sprintf("check timed out after %s", timeout)
			} else {
				item.Error = err.Error()
			}
			out.Errored = true
			out.OK = false
		default:
			item.Result = result
			if !result.OK {
				out.OK = false
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// checkTimeout parses the per-check time limit. Zero means no limit.
func (c NamedCheck) checkTimeout() (time.Duration, error) {
	if c.Timeout == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 0, fmt.Errorf("%w: check %q: invalid timeout %q: %v", ErrInvalidBatch, checkName(c), c.Timeout, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%w: check %q: timeout must not be negative, got %s", ErrInvalidBatch, checkName(c), d)
	}
	return d, nil
}

// withCheckTimeout bounds one check with its own deadline. A zero limit leaves
// the parent context as the only bound, so a shorter parent deadline still
// wins: context.WithTimeout keeps whichever comes first.
func withCheckTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// runBounded runs one check and reports its result as soon as ctx is done.
//
// The deadline is enforced here rather than by the RPC layer, and that is
// deliberate: the Stellar SDK's JSON-RPC channel posts with an http.Request
// carrying no context, so an in-flight request is only ever bounded by the
// per-request HTTP timeout, however tight the caller's context is. Selecting
// on ctx means a hung check costs its own budget and no more; the abandoned
// call still ends on the transport's own timeout, and nothing it later
// returns is read.
//
// ctx must carry a deadline, as the per-check deadline always does when there
// is a limit; with none, this is a plain call.
func runBounded(ctx context.Context, p *Prober, req CheckRequest) (*CheckResult, error) {
	type outcome struct {
		result *CheckResult
		err    error
	}

	done := make(chan outcome, 1) // buffered: the runner must not leak when its result is abandoned
	go func() {
		result, err := p.Check(ctx, req)
		done <- outcome{result, err}
	}()

	select {
	case o := <-done:
		return o.result, o.err
	case <-ctx.Done():
		select {
		case o := <-done: // finished as the deadline passed; the answer is still the answer
			return o.result, o.err
		default:
		}
		return nil, ctx.Err()
	}
}

func checkName(c NamedCheck) string {
	if c.Name != "" {
		return c.Name
	}
	return c.ContractID
}

func validateBatch(req BatchRequest) error {
	switch {
	case len(req.Checks) == 0:
		return fmt.Errorf("%w: no checks given", ErrInvalidBatch)
	case len(req.Checks) > MaxBatchChecks:
		return fmt.Errorf("%w: %d checks exceeds the limit of %d", ErrInvalidBatch, len(req.Checks), MaxBatchChecks)
	}

	seen := make(map[string]int, len(req.Checks))
	for i, c := range req.Checks {
		if c.ContractID == "" {
			return fmt.Errorf("%w: check %d has no contract_id", ErrInvalidBatch, i)
		}
		if _, err := c.checkTimeout(); err != nil {
			return err
		}
		if c.ExpectWasmHash != "" {
			if _, err := normalizeWasmHash(c.ExpectWasmHash); err != nil {
				return fmt.Errorf("%w: check %q: %v", ErrInvalidBatch, checkName(c), err)
			}
		}
		switch c.DataDurability {
		case "", health.DurabilityPersistent, health.DurabilityTemporary:
		default:
			return fmt.Errorf("%w: check %q: unknown data_durability %q (want persistent or temporary)",
				ErrInvalidBatch, checkName(c), c.DataDurability)
		}
		name := checkName(c)
		if prev, dup := seen[name]; dup {
			return fmt.Errorf("%w: checks %d and %d are both named %q; names must be unique",
				ErrInvalidBatch, prev, i, name)
		}
		seen[name] = i
	}
	return nil
}
