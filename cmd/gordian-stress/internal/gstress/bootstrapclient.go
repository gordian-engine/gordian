package gstress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/tv42/httpunix"
)

// BootstrapClient returns a client to connect to a bootstrap host,
// over the host's unix socket.
//
// Method on BootstrapClient are not safe for concurrent use.
type BootstrapClient struct {
	log *slog.Logger

	reg gcrypto.Registry

	client *http.Client
}

const bootstrapURL = "http+unix://bootstraphost"

func NewBootstrapClient(log *slog.Logger, serverSocketPath string) (*BootstrapClient, error) {
	// We are using a HTTP client over a Unix socket,
	// as this is a bit simpler from the end user's perspective.
	tr := &httpunix.Transport{
		DialTimeout:           100 * time.Millisecond,
		RequestTimeout:        time.Second,
		ResponseHeaderTimeout: time.Second,
	}
	// When encountering a URL with the domain "bootstraphost",
	// access it through the serverSocketPath.
	tr.RegisterLocation("bootstraphost", serverSocketPath)

	c := &BootstrapClient{
		log: log,

		client: &http.Client{Transport: tr},
	}

	gcrypto.RegisterEd25519(&c.reg)

	return c, nil
}

func (c *BootstrapClient) SeedAddrs() ([]string, error) {
	resp, err := c.client.Get(bootstrapURL + "/seed-addrs")
	if err != nil {
		return nil, fmt.Errorf("failed to get seed addresses: %w", err)
	}

	defer resp.Body.Close()

	var addrs []string

	if err := json.NewDecoder(resp.Body).Decode(&addrs); err != nil {
		return nil, fmt.Errorf("failed to parse seed addresses: %w", err)
	}

	return addrs, nil
}

func (c *BootstrapClient) ChainID() (string, error) {
	resp, err := c.client.Get(bootstrapURL + "/chain-id")
	if err != nil {
		return "", fmt.Errorf("failed to get chain ID: %w", err)
	}

	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read chain ID response: %w", err)
	}

	id, ok := strings.CutSuffix(string(b), "\n")
	if !ok {
		return "", fmt.Errorf("got malformatted chain ID: %q", b)
	}

	return id, nil
}

func (c *BootstrapClient) SetChainID(newID string) error {
	resp, err := c.client.Post(
		bootstrapURL+"/chain-id",
		"text/plain",
		strings.NewReader(newID+"\n"),
	)
	if err != nil {
		return fmt.Errorf("failed to get chain ID: %w", err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("got unexpected response status: %d", resp.StatusCode)
	}

	return nil
}

func (c *BootstrapClient) App() (string, error) {
	resp, err := c.client.Get(bootstrapURL + "/app")
	if err != nil {
		return "", fmt.Errorf("failed to get app: %w", err)
	}

	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read app response: %w", err)
	}

	app, ok := strings.CutSuffix(string(b), "\n")
	if !ok {
		return "", fmt.Errorf("got malformatted app: %q", b)
	}

	return app, nil
}

func (c *BootstrapClient) SetApp(a string) error {
	resp, err := c.client.Post(
		bootstrapURL+"/app",
		"text/plain",
		strings.NewReader(a+"\n"),
	)
	if err != nil {
		return fmt.Errorf("failed to get app: %w", err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("got unexpected response status: %d", resp.StatusCode)
	}

	return nil
}

func (c *BootstrapClient) RegisterValidator(v tmconsensus.Validator) error {
	jv := jsonValidator{
		ValPubKeyBytes: c.reg.Marshal(v.PubKey),
		Power:          v.Power,
	}

	b, err := json.Marshal(jv)
	if err != nil {
		return fmt.Errorf("failed to marshal validator: %w", err)
	}

	resp, err := c.client.Post(
		bootstrapURL+"/register-validator",
		"application/json",
		bytes.NewReader(b),
	)
	if err != nil {
		return fmt.Errorf("failed to post validator: %w", err)
	}

	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("got unexpected response status: %d", resp.StatusCode)
	}

	return nil
}

func (c *BootstrapClient) Validators() ([]tmconsensus.Validator, error) {
	resp, err := c.client.Get(bootstrapURL + "/validators")
	if err != nil {
		return nil, fmt.Errorf("failed to get validators: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("got unexpected response status: %d", resp.StatusCode)
	}

	var jvs []jsonValidator
	if err := json.NewDecoder(resp.Body).Decode(&jvs); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	vals := make([]tmconsensus.Validator, len(jvs))
	for i, jv := range jvs {
		pubKey, err := c.reg.Unmarshal(jv.ValPubKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed decoding validator public key %q: %w", jv.ValPubKeyBytes, err)
		}
		vals[i] = tmconsensus.Validator{
			PubKey: pubKey,
			Power:  jv.Power,
		}
	}

	return vals, nil
}

func (c *BootstrapClient) Start() error {
	resp, err := c.client.Post(bootstrapURL+"/start", "", nil)
	if err != nil {
		return fmt.Errorf("failed to make start call: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("got unexpected response status: %d", resp.StatusCode)
	}

	return nil
}

func (c *BootstrapClient) Halt() error {
	resp, err := c.client.Post(bootstrapURL+"/halt", "", nil)
	if err != nil {
		return fmt.Errorf("failed to make halt call: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("got unexpected response status: %d", resp.StatusCode)
	}

	return nil
}
