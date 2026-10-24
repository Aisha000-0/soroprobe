package probe

import (
	"context"
	"errors"
	"fmt"
)

// Readiness failures. They classify why an RPC endpoint is not ready to
// answer queries, so an HTTP layer can put a reason on a 503 without parsing
// error text.
var (
	// ErrRPCUnreachable reports that getHealth or getNetwork could not be
	// answered at all: the endpoint is down, timing out, or refusing
	// connections.
	ErrRPCUnreachable = errors.New("rpc unreachable")
	// ErrRPCUnhealthy reports that getHealth answered with a status other
	// than "healthy".
	ErrRPCUnhealthy = errors.New("rpc unhealthy")
	// ErrWrongNetwork reports that the RPC endpoint serves a different
	// network than the one the Prober is configured for. Probing it would
	// answer questions about the wrong chain.
	ErrWrongNetwork = errors.New("wrong network")
)

// Ready reports whether the RPC endpoint behind this Prober is ready to
// answer queries: it must report healthy through getHealth and serve the
// network wantPassphrase names.
//
// Ready describes the endpoint, not any contract, and the returned error
// wraps one of ErrRPCUnreachable, ErrRPCUnhealthy or ErrWrongNetwork so
// callers can classify it with errors.Is. Both RPC methods are read-only;
// Ready never submits anything.
func (p *Prober) Ready(ctx context.Context, wantPassphrase string) error {
	health, err := p.client.GetHealth(ctx)
	if err != nil {
		return fmt.Errorf("%w: getHealth: %w", ErrRPCUnreachable, err)
	}
	if health.Status != "healthy" {
		return fmt.Errorf("%w: getHealth reported status %q", ErrRPCUnhealthy, health.Status)
	}

	network, err := p.client.GetNetwork(ctx)
	if err != nil {
		return fmt.Errorf("%w: getNetwork: %w", ErrRPCUnreachable, err)
	}
	if network.Passphrase != wantPassphrase {
		return fmt.Errorf("%w: rpc serves %q, configured %q",
			ErrWrongNetwork, network.Passphrase, wantPassphrase)
	}
	return nil
}
