package abi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBytes caps a SoroVault response. A large contract interface is
// tens of kilobytes; anything far beyond that is not a response worth parsing.
const maxResponseBytes = 4 << 20

// SoroVault reads contract interfaces from a SoroVault registry's JSON API
// (GET /api/contracts/{id}).
type SoroVault struct {
	baseURL string
	http    *http.Client
}

var _ Source = (*SoroVault)(nil)

// NewSoroVault returns a Source backed by the SoroVault instance at baseURL,
// for example "http://localhost:8080". A nil client gets one with timeout as
// its overall request deadline.
func NewSoroVault(baseURL string, client *http.Client, timeout time.Duration) (*SoroVault, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("sorovault url %q must be an absolute http:// or https:// URL", baseURL)
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &SoroVault{baseURL: u.String(), http: client}, nil
}

// NetworkName maps a network passphrase onto the label SoroVault files
// contracts under. Unrecognized passphrases are used verbatim, which is also
// what SoroVault does.
func NetworkName(passphrase string) string {
	switch passphrase {
	case "Public Global Stellar Network ; September 2015":
		return "public"
	case "Test SDF Network ; September 2015":
		return "testnet"
	case "Test SDF Future Network ; October 2022":
		return "futurenet"
	default:
		return passphrase
	}
}

// Interface implements Source.
func (s *SoroVault) Interface(ctx context.Context, network, contractID string) (*Interface, error) {
	endpoint := s.baseURL + "/api/contracts/" + url.PathEscape(contractID)
	if network != "" {
		endpoint += "?network=" + url.QueryEscape(network)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sorovault: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // the body has been read; a close error is not actionable here.

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("sorovault: reading response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s on %s", ErrUnknownContract, contractID, network)
	case http.StatusUnprocessableEntity:
		// Registered, but there is no interface to read: a Stellar asset
		// contract, or a module without a spec section. Equivalent to not
		// knowing it, for typing purposes.
		return nil, fmt.Errorf("%w: %s", ErrUnknownContract, errorMessage(body, resp.Status))
	default:
		return nil, fmt.Errorf("sorovault: %s", errorMessage(body, resp.Status))
	}

	var payload struct {
		Interface *Interface `json:"interface"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("sorovault: decoding response: %w", err)
	}
	if payload.Interface == nil {
		return nil, errors.New("sorovault: response carried no interface")
	}
	return payload.Interface, nil
}

// errorMessage extracts SoroVault's {"error": "..."} body, falling back to
// the HTTP status when the body is not in that shape.
func errorMessage(body []byte, status string) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return status
}
