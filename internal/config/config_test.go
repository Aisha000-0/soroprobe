package config_test

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/config"
)

// writeConfig writes a config file into a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "soroprobe.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestDefaultsTargetTestnet(t *testing.T) {
	cfg := config.Default()

	assert.Equal(t, config.DefaultRPCURL, cfg.RPCURL)
	assert.Equal(t, "Test SDF Network ; September 2015", cfg.NetworkPassphrase)
	assert.Equal(t, 30*time.Second, cfg.Timeout)
	// A fresh checkout must be runnable with no configuration at all.
	require.NoError(t, cfg.Validate())
}

func TestDefaultSourceAccountIsValid(t *testing.T) {
	// Simulation needs a well-formed source account but never a funded one, so
	// the default placeholder still has to be a real strkey public key.
	cfg := config.Default()
	assert.Len(t, cfg.SourceAccount, 56)
	assert.NoError(t, cfg.Validate())
}

func TestDefaultHTTPAddrIsLoopback(t *testing.T) {
	// SoroProbe is read-only, but an instance reachable from the network still
	// spends the operator's RPC quota on whoever finds it, so the default
	// serves the local machine only. Serving wider than that is deliberate:
	// --addr 0.0.0.0:8080.
	cfg := config.Default()
	assert.Equal(t, "127.0.0.1:8080", cfg.HTTPAddr)

	host, port, err := net.SplitHostPort(cfg.HTTPAddr)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
	assert.Equal(t, "8080", port)
	assert.NoError(t, cfg.Validate())
}

func TestLoadWithNoFileOrEnvGivesDefaults(t *testing.T) {
	// An absent ./soroprobe.json is not an error; an explicitly named one is.
	t.Chdir(t.TempDir())

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, config.Default(), cfg)
}

func TestLoadMissingExplicitFileIsAnError(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "nope.json"))
	require.Error(t, err)
}

func TestLoadFromFile(t *testing.T) {
	path := writeConfig(t, `{
		"rpc_url": "https://rpc.example.com",
		"network_passphrase": "Custom Network",
		"http_addr": ":9999",
		"log_level": "debug",
		"timeout": "5s",
		"warn_ledgers": 100,
		"critical_ledgers": 10,
		"sorovault_url": "http://vault.internal:8080"
	}`)

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "https://rpc.example.com", cfg.RPCURL)
	assert.Equal(t, "Custom Network", cfg.NetworkPassphrase)
	assert.Equal(t, ":9999", cfg.HTTPAddr)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 5*time.Second, cfg.Timeout)
	assert.EqualValues(t, 100, cfg.WarnLedgers)
	assert.EqualValues(t, 10, cfg.CriticalLedgers)
	assert.Equal(t, "http://vault.internal:8080", cfg.SoroVaultURL)
	// Unset keys keep their defaults.
	assert.Equal(t, config.DefaultSourceAccount, cfg.SourceAccount)
}

func TestLoadFileFoundViaEnvVar(t *testing.T) {
	path := writeConfig(t, `{"rpc_url":"https://from-env-file.example.com"}`)
	t.Setenv(config.EnvConfigFile, path)

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, "https://from-env-file.example.com", cfg.RPCURL)
}

func TestEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, `{"rpc_url":"https://from-file.example.com","log_level":"debug"}`)
	t.Setenv(config.EnvRPCURL, "https://from-env.example.com")

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "https://from-env.example.com", cfg.RPCURL, "env must win over the file")
	assert.Equal(t, "debug", cfg.LogLevel, "keys not set in env keep the file value")
}

func TestEnvVarsAreRead(t *testing.T) {
	t.Setenv(config.EnvRPCURL, "https://rpc.example.com")
	t.Setenv(config.EnvNetworkPassphrase, "Public Global Stellar Network ; September 2015")
	t.Setenv(config.EnvSourceAccount, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF")
	t.Setenv(config.EnvHTTPAddr, ":7000")
	t.Setenv(config.EnvCORSOrigins, "https://dashboard.example, https://admin.example")
	t.Setenv(config.EnvLogLevel, "warn")
	t.Setenv(config.EnvTimeout, "90s")
	t.Setenv(config.EnvWarnLedgers, "500")
	t.Setenv(config.EnvCriticalLedgers, "50")
	t.Setenv(config.EnvSoroVaultURL, "https://vault.example.com")
	t.Chdir(t.TempDir())

	cfg, err := config.Load("")
	require.NoError(t, err)

	assert.Equal(t, "https://rpc.example.com", cfg.RPCURL)
	assert.Equal(t, ":7000", cfg.HTTPAddr)
	assert.Equal(t, []string{"https://dashboard.example", "https://admin.example"}, cfg.CORSOrigins)
	assert.Equal(t, "warn", cfg.LogLevel)
	assert.Equal(t, 90*time.Second, cfg.Timeout)
	assert.EqualValues(t, 500, cfg.WarnLedgers)
	assert.EqualValues(t, 50, cfg.CriticalLedgers)
	assert.Equal(t, "https://vault.example.com", cfg.SoroVaultURL)
	require.NoError(t, cfg.Validate())
}

func TestEmptyEnvVarIsIgnored(t *testing.T) {
	// An exported-but-empty variable, as a shell can easily produce, must not
	// wipe out the default.
	t.Setenv(config.EnvRPCURL, "")
	t.Chdir(t.TempDir())

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, config.DefaultRPCURL, cfg.RPCURL)
}

func TestLoadRejectsMalformedInput(t *testing.T) {
	t.Run("bad json", func(t *testing.T) {
		_, err := config.Load(writeConfig(t, `{not json`))
		require.Error(t, err)
	})

	t.Run("bad duration in file", func(t *testing.T) {
		_, err := config.Load(writeConfig(t, `{"timeout":"soon"}`))
		require.Error(t, err)
	})

	t.Run("bad duration in env", func(t *testing.T) {
		t.Setenv(config.EnvTimeout, "soon")
		t.Chdir(t.TempDir())
		_, err := config.Load("")
		require.Error(t, err)
	})

	t.Run("non-numeric ledger threshold", func(t *testing.T) {
		t.Setenv(config.EnvWarnLedgers, "lots")
		t.Chdir(t.TempDir())
		_, err := config.Load("")
		require.Error(t, err)
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string
	}{
		{"empty rpc url", func(c *config.Config) { c.RPCURL = "" }, "rpc url is required"},
		{"rpc url without scheme", func(c *config.Config) { c.RPCURL = "rpc.example.com" }, "http://"},
		{"empty passphrase", func(c *config.Config) { c.NetworkPassphrase = "" }, "passphrase is required"},
		{"source account not a key", func(c *config.Config) { c.SourceAccount = "nope" }, "not a valid ed25519 public key"},
		{
			// A secret key would work as a string but must be refused: SoroProbe
			// never needs one and accepting it invites users to paste one in.
			"secret key rejected as source account",
			func(c *config.Config) {
				c.SourceAccount = "SAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			},
			"not a valid ed25519 public key",
		},
		{"contract address rejected as source account", func(c *config.Config) {
			c.SourceAccount = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
		}, "not a valid ed25519 public key"},
		{"zero timeout", func(c *config.Config) { c.Timeout = 0 }, "timeout must be positive"},
		{"negative timeout", func(c *config.Config) { c.Timeout = -time.Second }, "timeout must be positive"},
		{"critical above warn", func(c *config.Config) { c.WarnLedgers = 10; c.CriticalLedgers = 100 }, "must not exceed"},
		{"unknown log level", func(c *config.Config) { c.LogLevel = "chatty" }, "unknown log level"},
		{"sorovault url without scheme", func(c *config.Config) { c.SoroVaultURL = "localhost:8080" }, "sorovault url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			tt.mutate(&cfg)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateAllowsEqualThresholds(t *testing.T) {
	cfg := config.Default()
	cfg.WarnLedgers = 100
	cfg.CriticalLedgers = 100
	assert.NoError(t, cfg.Validate())
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{" info ", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := config.ParseLogLevel(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	_, err := config.ParseLogLevel("chatty")
	assert.Error(t, err)
}

func TestLoadAddressBook(t *testing.T) {
	path := writeConfig(t, `{
		"address_book": {
			"alice": "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
			"token": "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
		}
	}`)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.AddressBook, 2)
	assert.Equal(t, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF", cfg.AddressBook["alice"])
	assert.NoError(t, cfg.Validate())
}

func TestLoadRejectsInvalidAddressBookEntry(t *testing.T) {
	// Validation happens at load time, so a bad key fails before any probe
	// runs rather than on first use of that name.
	path := writeConfig(t, `{"address_book": {"alice": "not-an-address"}}`)

	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid account")
}

func TestValidateRejectsInvalidAddressBookEntry(t *testing.T) {
	cfg := config.Default()
	cfg.AddressBook = map[string]string{"alice": "not-an-address"}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid account")
}
