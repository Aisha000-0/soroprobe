// Package config resolves SoroProbe's runtime settings from defaults, an
// optional config file, and the environment.
//
// Precedence, lowest to highest: defaults, config file, environment. Command
// line flags sit above all of these and are applied by the caller (the CLI
// binds them directly onto the returned Config).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
)

// Defaults. These target the public testnet so that a fresh checkout can run
// the quickstart without any configuration at all.
const (
	DefaultRPCURL            = "https://soroban-testnet.stellar.org"
	DefaultNetworkPassphrase = network.TestNetworkPassphrase
	// DefaultHTTPAddr is loopback on purpose. SoroProbe is read-only, but an
	// instance reachable from the network still spends the operator's RPC
	// quota on whoever finds it, so serving wider than the local machine is an
	// opt-in rather than the default. The Docker image widens it, because
	// loopback inside a container would reach nothing.
	DefaultHTTPAddr = "127.0.0.1:8080"
	DefaultLogLevel = "info"
	DefaultTimeout  = 30 * time.Second

	// DefaultSourceAccount is the all-zero ed25519 account ID. Simulation reads
	// the source account but never needs it to exist, be funded, or sign
	// anything, so a placeholder is a safe default. Override it when a contract
	// authorizes on the invoker's address.
	DefaultSourceAccount = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

	// DefaultWarnLedgers is ~7 days at 5s per ledger.
	DefaultWarnLedgers = 120_960
	// DefaultCriticalLedgers is ~1 day at 5s per ledger.
	DefaultCriticalLedgers = 17_280
	// DefaultBaseFee is the network minimum base fee in stroops.
	DefaultBaseFee = int64(100)
)

// Environment variable names.
const (
	EnvRPCURL            = "RPC_URL"
	EnvNetworkPassphrase = "NETWORK_PASSPHRASE"
	EnvSourceAccount     = "SOURCE_ACCOUNT"
	EnvHTTPAddr          = "HTTP_ADDR"
	EnvCORSOrigins       = "CORS_ORIGINS"
	EnvLogLevel          = "LOG_LEVEL"
	EnvTimeout           = "RPC_TIMEOUT"
	EnvWarnLedgers       = "WARN_LEDGERS"
	EnvCriticalLedgers   = "CRITICAL_LEDGERS"
	EnvConfigFile        = "SOROPROBE_CONFIG"
	EnvSoroVaultURL      = "SOROVAULT_URL"
	EnvBaseFee           = "BASE_FEE"
	EnvInspectCacheTTL   = "INSPECT_CACHE_TTL"
)

// Config holds every setting SoroProbe needs.
type Config struct {
	// RPCURL is the Stellar RPC endpoint to query.
	RPCURL string `json:"rpc_url"`
	// NetworkPassphrase identifies the network transactions are built for.
	NetworkPassphrase string `json:"network_passphrase"`
	// SourceAccount is the public key used as the transaction source when
	// building an invocation to simulate. A secret key is never required.
	SourceAccount string `json:"source_account"`
	// HTTPAddr is the listen address for the HTTP API.
	HTTPAddr string `json:"http_addr"`
	// CORSOrigins is the exact-origin allow-list for the HTTP API.
	CORSOrigins []string `json:"-"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `json:"log_level"`
	// Timeout bounds a single RPC request.
	Timeout time.Duration `json:"timeout"`
	// WarnLedgers is the remaining-TTL threshold, in ledgers, below which an
	// entry is reported as a warning.
	WarnLedgers uint32 `json:"warn_ledgers"`
	// CriticalLedgers is the remaining-TTL threshold, in ledgers, below which
	// an entry is reported as critical.
	CriticalLedgers uint32 `json:"critical_ledgers"`
	// SoroVaultURL, when set, is a SoroVault registry whose recorded contract
	// interfaces type simulate arguments. Empty disables the lookup.
	SoroVaultURL string `json:"sorovault_url"`
	// InspectCacheTTL is how long `serve` may reuse an inspect result for the
	// same contract and key set. Zero, the default, disables the cache; a few
	// seconds suits dashboards, because TTLs change at most once per ledger.
	InspectCacheTTL time.Duration `json:"inspect_cache_ttl"`

	// BaseFee is the base inclusion fee per operation in stroops.
	BaseFee int64 `json:"base_fee"`
}

// Default returns a Config populated entirely from the package defaults.
func Default() Config {
	return Config{
		RPCURL:            DefaultRPCURL,
		NetworkPassphrase: DefaultNetworkPassphrase,
		SourceAccount:     DefaultSourceAccount,
		HTTPAddr:          DefaultHTTPAddr,
		LogLevel:          DefaultLogLevel,
		Timeout:           DefaultTimeout,
		WarnLedgers:       DefaultWarnLedgers,
		CriticalLedgers:   DefaultCriticalLedgers,
		BaseFee:           DefaultBaseFee,
	}
}

// fileConfig mirrors Config but with pointers, so that "absent" is
// distinguishable from "set to the zero value".
type fileConfig struct {
	RPCURL            *string `json:"rpc_url"`
	NetworkPassphrase *string `json:"network_passphrase"`
	SourceAccount     *string `json:"source_account"`
	HTTPAddr          *string `json:"http_addr"`
	LogLevel          *string `json:"log_level"`
	Timeout           *string `json:"timeout"`
	WarnLedgers       *uint32 `json:"warn_ledgers"`
	CriticalLedgers   *uint32 `json:"critical_ledgers"`
	SoroVaultURL      *string `json:"sorovault_url"`
	BaseFee           *int64  `json:"base_fee"`
	InspectCacheTTL   *string `json:"inspect_cache_ttl"`
}

// Load builds a Config from defaults, then the config file at path (if
// non-empty, or if SOROPROBE_CONFIG names one), then the environment.
//
// A path given explicitly must exist; a path discovered via SOROPROBE_CONFIG
// must also exist. Only the implicit lookup of ./soroprobe.json is allowed to
// come up empty.
func Load(path string) (Config, error) {
	cfg := Default()

	explicit := path != ""
	if !explicit {
		path = os.Getenv(EnvConfigFile)
		explicit = path != ""
	}
	if !explicit {
		path = "soroprobe.json"
	}

	if err := applyFile(&cfg, path, explicit); err != nil {
		return cfg, err
	}
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyFile(cfg *Config, path string, required bool) error {
	raw, err := os.ReadFile(path) //nolint:gosec // the config path comes from --config or SOROPROBE_CONFIG; reading it is the feature.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil
		}
		return fmt.Errorf("read config file %s: %w", path, err)
	}

	var fc fileConfig
	if err := json.Unmarshal(raw, &fc); err != nil {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}

	setString(&cfg.RPCURL, fc.RPCURL)
	setString(&cfg.NetworkPassphrase, fc.NetworkPassphrase)
	setString(&cfg.SourceAccount, fc.SourceAccount)
	setString(&cfg.HTTPAddr, fc.HTTPAddr)
	setString(&cfg.LogLevel, fc.LogLevel)
	setString(&cfg.SoroVaultURL, fc.SoroVaultURL)
	if fc.WarnLedgers != nil {
		cfg.WarnLedgers = *fc.WarnLedgers
	}
	if fc.CriticalLedgers != nil {
		cfg.CriticalLedgers = *fc.CriticalLedgers
	}
	if fc.BaseFee != nil {
		cfg.BaseFee = *fc.BaseFee
	}
	if fc.Timeout != nil {
		d, err := time.ParseDuration(*fc.Timeout)
		if err != nil {
			return fmt.Errorf("parse config file %s: timeout: %w", path, err)
		}
		cfg.Timeout = d
	}
	if fc.InspectCacheTTL != nil {
		d, err := time.ParseDuration(*fc.InspectCacheTTL)
		if err != nil {
			return fmt.Errorf("parse config file %s: inspect_cache_ttl: %w", path, err)
		}
		cfg.InspectCacheTTL = d
	}
	return nil
}

func applyEnv(cfg *Config) error {
	setString(&cfg.RPCURL, lookup(EnvRPCURL))
	setString(&cfg.NetworkPassphrase, lookup(EnvNetworkPassphrase))
	setString(&cfg.SourceAccount, lookup(EnvSourceAccount))
	setString(&cfg.HTTPAddr, lookup(EnvHTTPAddr))
	if origins := lookup(EnvCORSOrigins); origins != nil {
		cfg.CORSOrigins = nil
		for _, origin := range strings.Split(*origins, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				cfg.CORSOrigins = append(cfg.CORSOrigins, origin)
			}
		}
	}
	setString(&cfg.LogLevel, lookup(EnvLogLevel))
	setString(&cfg.SoroVaultURL, lookup(EnvSoroVaultURL))

	if v := lookup(EnvTimeout); v != nil {
		d, err := time.ParseDuration(*v)
		if err != nil {
			return fmt.Errorf("parse %s: %w", EnvTimeout, err)
		}
		cfg.Timeout = d
	}
	if v := lookup(EnvInspectCacheTTL); v != nil {
		d, err := time.ParseDuration(*v)
		if err != nil {
			return fmt.Errorf("parse %s: %w", EnvInspectCacheTTL, err)
		}
		cfg.InspectCacheTTL = d
	}
	if err := setUint32FromEnv(&cfg.WarnLedgers, EnvWarnLedgers); err != nil {
		return err
	}
	if err := setUint32FromEnv(&cfg.CriticalLedgers, EnvCriticalLedgers); err != nil {
		return err
	}
	return setInt64FromEnv(&cfg.BaseFee, EnvBaseFee)
}

func setInt64FromEnv(dst *int64, key string) error {
	v := lookup(key)
	if v == nil {
		return nil
	}
	n, err := strconv.ParseInt(*v, 10, 64)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	*dst = n
	return nil
}

func setUint32FromEnv(dst *uint32, key string) error {
	v := lookup(key)
	if v == nil {
		return nil
	}
	n, err := strconv.ParseUint(*v, 10, 32)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	*dst = uint32(n)
	return nil
}

func lookup(key string) *string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	return &v
}

func setString(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

// Validate reports whether the configuration is usable.
func (c Config) Validate() error {
	if c.RPCURL == "" {
		return errors.New("rpc url is required")
	}
	if !strings.HasPrefix(c.RPCURL, "http://") && !strings.HasPrefix(c.RPCURL, "https://") {
		return fmt.Errorf("rpc url %q must start with http:// or https://", c.RPCURL)
	}
	if c.NetworkPassphrase == "" {
		return errors.New("network passphrase is required")
	}
	if !strkey.IsValidEd25519PublicKey(c.SourceAccount) {
		return fmt.Errorf("source account %q is not a valid ed25519 public key (G...)", c.SourceAccount)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive, got %s", c.Timeout)
	}
	if c.CriticalLedgers > c.WarnLedgers {
		return fmt.Errorf("critical threshold (%d) must not exceed warning threshold (%d)",
			c.CriticalLedgers, c.WarnLedgers)
	}
	if c.InspectCacheTTL < 0 {
		return fmt.Errorf("inspect cache ttl must not be negative, got %s", c.InspectCacheTTL)
	}
	if _, err := ParseLogLevel(c.LogLevel); err != nil {
		return err
	}
	if c.SoroVaultURL != "" &&
		!strings.HasPrefix(c.SoroVaultURL, "http://") && !strings.HasPrefix(c.SoroVaultURL, "https://") {
		return fmt.Errorf("sorovault url %q must start with http:// or https://", c.SoroVaultURL)
	}
	return nil
}

// ParseLogLevel maps a log level name onto an slog.Level.
func ParseLogLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", name)
	}
}

// Logger builds the process logger described by this config.
func (c Config) Logger(w *os.File) *slog.Logger {
	level, err := ParseLogLevel(c.LogLevel)
	if err != nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
