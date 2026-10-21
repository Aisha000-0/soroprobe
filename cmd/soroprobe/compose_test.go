package main

// The docker-compose.yml must keep its promise to `soroprobe serve`: build
// this checkout, run serve, pass the documented environment through, and
// probe /healthz. Compose interpolates and validates the file only at
// `docker compose up`, so these tests parse it on every `make check` and
// catch drift without a Docker daemon.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// composeFile models just enough of the Compose schema for the checks below.
type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Build       string            `yaml:"build"`
	Command     []string          `yaml:"command"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Healthcheck *composeHealth    `yaml:"healthcheck"`
}

type composeHealth struct {
	Test     []string `yaml:"test"`
	Interval string   `yaml:"interval"`
	Timeout  string   `yaml:"timeout"`
	Retries  int      `yaml:"retries"`
}

// loadCompose parses the repository's docker-compose.yml.
func loadCompose(t *testing.T) composeFile {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	require.NoError(t, err, "docker-compose.yml must exist at the repository root")

	var file composeFile
	require.NoError(t, yaml.Unmarshal(raw, &file), "docker-compose.yml must be valid YAML")
	return file
}

func TestComposeBuildsAndRunsServe(t *testing.T) {
	file := loadCompose(t)

	svc, ok := file.Services["api"]
	require.True(t, ok, "docker-compose.yml must define an api service")
	assert.Equal(t, ".", svc.Build, "the image must build from this checkout")
	assert.Equal(t, []string{"serve"}, svc.Command, "the container must run `soroprobe serve`")
	assert.Contains(t, svc.Ports, "8080:8080", "the API must be published on localhost:8080")
}

func TestComposePassesTheEnvironmentThrough(t *testing.T) {
	svc := loadCompose(t).Services["api"]

	// Each variable is interpolated from the host environment, so the same
	// file works against any network.
	for _, key := range []string{"RPC_URL", "NETWORK_PASSPHRASE", "SOROVAULT_URL"} {
		value, ok := svc.Environment[key]
		require.True(t, ok, "%s must be passed through to the container", key)
		assert.Contains(t, value, "${"+key, "%s must come from the environment", key)
	}

	// The defaults target the public testnet, so `docker compose up` works
	// with no configuration at all.
	assert.Contains(t, svc.Environment["RPC_URL"], "soroban-testnet.stellar.org")
	assert.Contains(t, svc.Environment["NETWORK_PASSPHRASE"], "Test SDF Network")

	// Inside the container the API must listen on every interface for the
	// port publish above to reach it.
	assert.Equal(t, ":8080", svc.Environment["HTTP_ADDR"])
}

func TestComposeHealthcheckProbesHealthz(t *testing.T) {
	svc := loadCompose(t).Services["api"]

	require.NotNil(t, svc.Healthcheck, "the api service must carry a healthcheck")
	require.Len(t, svc.Healthcheck.Test, 2)
	assert.Equal(t, "CMD-SHELL", svc.Healthcheck.Test[0])
	// Liveness, not readiness: /healthz never calls the network, so a brief
	// RPC blip must not restart the container.
	assert.Contains(t, svc.Healthcheck.Test[1], "/healthz")
	assert.NotContains(t, svc.Healthcheck.Test[1], "/readyz")
	assert.NotEmpty(t, svc.Healthcheck.Interval)
	assert.NotEmpty(t, svc.Healthcheck.Timeout)
	assert.Positive(t, svc.Healthcheck.Retries)
}
