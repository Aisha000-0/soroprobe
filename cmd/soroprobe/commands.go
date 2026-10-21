package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/api"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = ""

const argHelp = `Arguments use a "type:value" form. Supported types:

  bool:true          void                 null
  u32:5              i32:-5               u64:100        i64:-100
  u128:10000000      i128:-10000000       u256:...       i256:...
  timepoint:1700000000                    duration:3600
  sym:transfer       str:"hello world"    bytes:deadbeef
  addr:GABC...       addr:CDEF...
  'vec:["u32:1","u32:2"]'                 'map:[["sym:a","u32:1"]]'

Collections are JSON; each string element is itself an argument spec, and a
JSON number, true, false or null is a bare literal. Write map keys in
ascending order: Soroban rejects an unsorted map.

A bare value with no prefix is inferred: "true" and "false" become bool, "void"
and "null" become void, a valid G... or C... address becomes an address, a run
of digits becomes i128, and anything else becomes a symbol if it fits Soroban's
symbol rules or a string otherwise.

Prefer explicit prefixes. A contract expecting u32 will fail confusingly if it
is handed an inferred i128.`

func newSimulateCmd(opts *options) *cobra.Command {
	var reportPath string

	cmd := &cobra.Command{
		Use:   "simulate <contract-id> <function> [args...]",
		Short: "Simulate a contract invocation and report its result and cost",
		Long: `Build a contract invocation, simulate it against the network, and report
whether it would succeed, what it would return, and what it would cost.

Nothing is submitted and no signing takes place.

` + argHelp,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := openReport(reportPath)
			if err != nil {
				return err
			}
			defer report.close()

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := p.Simulate(cmd.Context(), probe.SimulateRequest{
				ContractID: args[0],
				Function:   args[1],
				Args:       args[2:],
			})
			if err != nil {
				return err
			}

			if err := report.write(result); err != nil {
				return err
			}

			if opts.jsonOut {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			return renderSimulate(cmd.OutOrStdout(), result)
		},
	}

	cmd.Flags().StringVar(&reportPath, "report", "", "write the JSON result to this file while printing text as usual")
	return cmd
}

func newInspectCmd(opts *options) *cobra.Command {
	var dataKeys []string
	var keyFile string
	var durability string
	var reportPath string

	cmd := &cobra.Command{
		Use:   "inspect <contract-id>",
		Short: "Read a contract's state entries and report expiration health",
		Long: `Read a contract's instance and code entries, and report how close each is to
expiring.

Stellar RPC reads ledger entries by key and cannot enumerate a contract's data
entries, so data entries must be named explicitly with --key. Listing every
entry a contract owns requires an indexer, which SoroProbe deliberately does not
depend on.

--key-file reads the same specs from a file, one per line, for contracts with
too many entries for a command line. Keys from the file and from --key are
combined; an invalid line is reported with its line number before any request
is sent.

` + argHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseDurability(durability)
			if err != nil {
				return err
			}

			report, err := openReport(reportPath)
			if err != nil {
				return err
			}
			defer report.close()

			keys, err := mergeKeys(keyFile, dataKeys)
			if err != nil {
				return err
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := p.Inspect(cmd.Context(), probe.InspectRequest{
				ContractID:     args[0],
				DataKeys:       keys,
				DataDurability: d,
			})
			if err != nil {
				return err
			}

			if err := report.write(result); err != nil {
				return err
			}

			if opts.jsonOut {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			return renderInspect(cmd.OutOrStdout(), result)
		},
	}

	cmd.Flags().StringArrayVar(&dataKeys, "key", nil, "a contract data key to inspect, in type:value form (repeatable)")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "a file listing data keys to inspect, one spec per line (blank lines and # comments ignored)")
	cmd.Flags().StringVar(&durability, "durability", "persistent", "durability for --key lookups: persistent or temporary")
	cmd.Flags().StringVar(&reportPath, "report", "", "write the JSON result to this file while printing text as usual")
	return cmd
}

func newCheckCmd(opts *options) *cobra.Command {
	var function string
	var callArgs []string
	var dataKeys []string
	var keyFile string
	var durability string
	var file string
	var checkTimeout time.Duration
	var reportPath string

	cmd := &cobra.Command{
		Use:   "check <contract-id> | check --file <checks.json>",
		Short: "Run a combined health check, exiting non-zero on failure",
		Long: `Run a combined health check against a contract: confirm it is deployed, that
its instance and code entries are live and not near expiration, and optionally
that a read-only call simulates successfully.

Exits 0 when every check passes, 1 when any check fails, and 2 when SoroProbe
itself could not run. A TTL warning does not fail the check; it is a signal to
extend the entry, not a reason to break a pipeline.

--fn is optional. SoroProbe cannot discover a contract's exported functions, and
guessing one would produce a misleading failure, so the simulation step is
reported as skipped unless a function is named.

--key and --key-file name data entries for the TTL check. --key-file reads one
spec per line from a file, ignoring blank lines and lines starting with #, and
combines them with --key. It applies to the single-contract form; --file
entries carry their own data_keys.

--file runs many checks at once, from a JSON file ("-" for stdin):

  {"checks": [
    {"name": "token", "contract_id": "C...", "function": "decimals"},
    {"name": "vault", "contract_id": "C...", "data_keys": ["sym:Admin"]}
  ]}

Each entry takes name, contract_id, function, args, data_keys, data_durability
and timeout — the same body POST /v1/checks accepts. timeout bounds a single
check, as a Go duration string ("45s"); a check that outlives it is reported as
one that could not run, and the batch continues. --check-timeout supplies the
default for entries that name none. With --file, the exit code is 2 if any
check could not run, else 1 if any failed, else 0.

--report writes the JSON result to a file while the text report still goes to
the terminal, so a CI run can keep readable logs and a machine-readable
artifact. The file is written even when the check fails.

Inside GitHub Actions (GITHUB_ACTIONS is set) each failed or warned step also
prints an ::error:: or ::warning:: line, which surfaces on the run summary
instead of hiding in the log. --json suppresses them.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if file != "" {
				if len(args) > 0 {
					return fmt.Errorf("give a contract ID or --file, not both")
				}
				if keyFile != "" {
					return fmt.Errorf("give --key-file with a contract ID, not with --file")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := openReport(reportPath)
			if err != nil {
				return err
			}
			defer report.close()

			if file != "" {
				return runCheckFile(cmd, opts, file, checkTimeout, report)
			}

			d, err := parseDurability(durability)
			if err != nil {
				return err
			}

			keys, err := mergeKeys(keyFile, dataKeys)
			if err != nil {
				return err
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := p.Check(cmd.Context(), probe.CheckRequest{
				ContractID:     args[0],
				Function:       function,
				Args:           callArgs,
				DataKeys:       keys,
				DataDurability: d,
			})
			if err != nil {
				return err
			}

			// Write the report before deciding the exit code, so a failed
			// check still leaves its report behind for the CI artifact.
			if err := report.write(result); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if opts.jsonOut {
				err = writeJSON(out, result)
			} else {
				err = renderCheck(out, result)
			}
			if err != nil {
				return err
			}

			// Annotations go to stdout, the stream a step's log reads. They
			// stay off under --json, whose consumer parses the document.
			if !opts.jsonOut {
				emitCheckAnnotations(out, result, inGitHubActions())
			}

			if !result.OK {
				return &checkFailedError{msg: fmt.Sprintf("check failed for %s", args[0])}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&function, "fn", "", "read-only function to simulate as a liveness probe")
	cmd.Flags().StringArrayVar(&callArgs, "arg", nil, "argument for --fn, in type:value form (repeatable)")
	cmd.Flags().StringArrayVar(&dataKeys, "key", nil, "a contract data key to include in the TTL check (repeatable)")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "a file listing data keys for the TTL check, one spec per line (blank lines and # comments ignored)")
	cmd.Flags().StringVar(&durability, "durability", "persistent", "durability for --key lookups: persistent or temporary")
	cmd.Flags().StringVarP(&file, "file", "f", "", `run every check listed in this JSON file ("-" for stdin)`)
	cmd.Flags().DurationVar(&checkTimeout, "check-timeout", 0,
		`default time limit for each check in --file; a per-entry "timeout" wins (0 means no limit)`)
	cmd.Flags().StringVar(&reportPath, "report", "", "write the JSON result to this file while printing text as usual")
	return cmd
}

// runCheckFile runs a batch of checks read from path.
func runCheckFile(cmd *cobra.Command, opts *options, path string, checkTimeout time.Duration, report *reportFile) error {
	req, err := readBatch(cmd.InOrStdin(), path)
	if err != nil {
		return err
	}
	applyDefaultTimeout(&req, checkTimeout)

	p, cleanup, err := opts.prober()
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := p.CheckBatch(cmd.Context(), req)
	if err != nil {
		return err
	}

	// Write the report before deciding the exit code, so a failed batch still
	// leaves its report behind for the CI artifact.
	if err := report.write(result); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if opts.jsonOut {
		err = writeJSON(out, result)
	} else {
		err = renderBatch(out, result)
	}
	if err != nil {
		return err
	}

	// Annotations go to stdout, the stream a step's log reads. They stay off
	// under --json, whose consumer parses the document.
	if !opts.jsonOut {
		emitBatchAnnotations(out, result, inGitHubActions())
	}

	switch {
	case result.Errored:
		// Returned as a plain error, so main exits with exitRuntime: the
		// batch is incomplete and its verdict cannot be trusted.
		return fmt.Errorf("one or more checks could not be run")
	case !result.OK:
		return &checkFailedError{msg: "one or more checks failed"}
	}
	return nil
}

// applyDefaultTimeout fills entries that name no timeout of their own with the
// --check-timeout default. A per-entry value always wins, so a batch can give
// one slow contract a longer budget than the rest.
func applyDefaultTimeout(req *probe.BatchRequest, d time.Duration) {
	if d <= 0 {
		return
	}
	for i := range req.Checks {
		if req.Checks[i].Timeout == "" {
			req.Checks[i].Timeout = d.String()
		}
	}
}

// readBatch parses a check file. Unknown fields are rejected, so a typo such
// as "fn" for "function" fails loudly instead of silently skipping a probe.
func readBatch(stdin io.Reader, path string) (probe.BatchRequest, error) {
	var r io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return probe.BatchRequest{}, err
		}
		defer f.Close()
		r = f
	}

	var req probe.BatchRequest
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return probe.BatchRequest{}, fmt.Errorf("read check file %s: %w", path, err)
	}
	return req, nil
}

// inGitHubActions reports whether GitHub Actions sets the marker variable.
// Only its presence matters: a step may set GITHUB_ACTIONS=false and still
// expect workflow commands to work.
func inGitHubActions() bool {
	_, ok := os.LookupEnv(annotationEnv)
	return ok
}

func newServeCmd(opts *options) *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP API",
		Long: `Serve the HTTP API, which mirrors the CLI for use from CI systems and other
services. The API is read-only, exactly like the CLI.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if addr != "" {
				opts.cfg.HTTPAddr = addr
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			log := opts.cfg.Logger(os.Stderr)
			server := api.New(api.Options{
				Prober: p,
				Addr:   opts.cfg.HTTPAddr,
				Logger: log,
			})
			return server.ListenAndServe(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "listen address, overriding HTTP_ADDR")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the SoroProbe version",
		Args:  cobra.NoArgs,
		// Version needs no network, so skip config resolution.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), resolveVersion())
			return err
		},
	}
}

// resolveVersion prefers a build-time version, falling back to the module
// version stamped into the binary by the Go toolchain.
func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "devel"
}

func parseDurability(s string) (health.Durability, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "persistent":
		return health.DurabilityPersistent, nil
	case "temporary", "temp":
		return health.DurabilityTemporary, nil
	default:
		return "", fmt.Errorf("unknown durability %q (want persistent or temporary)", s)
	}
}
