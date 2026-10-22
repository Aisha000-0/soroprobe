package probe

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// Timeout bounds this one check, as a Go duration string ("45s"). Empty
	// means no per-check limit beyond the context the caller passes; this is
	// what keeps one slow contract from consuming a whole batch's budget.
	Timeout      string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	CheckRequest `yaml:",inline"`
}

// BatchRequest is a list of checks to run together.
type BatchRequest struct {
	Checks      []NamedCheck `json:"checks" yaml:"checks"`
	Concurrency int          `json:"-" yaml:"-"`
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

// CheckBatch runs up to the requested number of checks and reports them in
// input order. One check failing to run does not stop the others; its error is
// recorded and the batch moves on. A check that outlives its own Timeout is
// reported as one that could not be run. Only a malformed batch or a canceled
// context returns a Go error.
func (p *Prober) CheckBatch(ctx context.Context, req BatchRequest) (*BatchResult, error) {
	if err := validateBatch(req); err != nil {
		return nil, err
	}
	if req.Concurrency < 0 {
		return nil, fmt.Errorf("%w: concurrency must not be negative", ErrInvalidBatch)
	}

	concurrency := req.Concurrency
	if concurrency == 0 {
		concurrency = 1
	}
	if concurrency > len(req.Checks) {
		concurrency = len(req.Checks)
	}

	items := make([]BatchItem, len(req.Checks))
	jobs := make(chan int, len(req.Checks))
	for i := range req.Checks {
		jobs <- i
	}
	close(jobs)

	var workers sync.WaitGroup
	workers.Add(concurrency)
	for worker := 0; worker < concurrency; worker++ {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case i, ok := <-jobs:
					if !ok || ctx.Err() != nil {
						return
					}
					c := req.Checks[i]
					timeout, _ := c.checkTimeout()
					checkCtx, cancel := withCheckTimeout(ctx, timeout)
					result, err := runBounded(checkCtx, p, c.CheckRequest)
					cancel()

					item := BatchItem{Name: checkName(c), ContractID: c.ContractID, Result: result}
					if err != nil {
						if ctx.Err() != nil {
							return
						}
						if timeout > 0 && errors.Is(err, context.DeadlineExceeded) {
							item.Error = fmt.Sprintf("check timed out after %s", timeout)
						} else {
							item.Error = err.Error()
						}
						item.Result = nil
					}
					items[i] = item
				}
			}
		}()
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out := &BatchResult{OK: true, Items: items}
	for _, item := range items {
		if item.Error != "" {
			out.Errored = true
			out.OK = false
		} else if !item.Result.OK {
			out.OK = false
		}
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
		return 0, fmt.Errorf("%w: check %q: invalid timeout %q: %w", ErrInvalidBatch, checkName(c), c.Timeout, err)
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
