// SPDX-License-Identifier: Apache-2.0

//go:build integration

package fakellmcontainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/fakellm"
)

const (
	port         = "8090/tcp"
	imageRepo    = "thnhdatle/fakellm"
	imageTag     = "integration"
	startTimeout = 10 * time.Minute
	stopTimeout  = time.Minute
)

// Container is a running fakellm container.
type Container struct {
	ctr *testcontainers.DockerContainer
	url string
	hc  *http.Client
}

// Start builds the fakellm image and starts a container, waiting until it is healthy.
func Start(ctx context.Context) (*Container, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	ctr, err := testcontainers.Run(ctx, "",
		testcontainers.WithDockerfile(testcontainers.FromDockerfile{
			Context: root, Dockerfile: "Dockerfile.fakellm",
			Repo: imageRepo, Tag: imageTag,
			KeepImage: true,
		}),
		testcontainers.WithExposedPorts(port),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort(port).
			WithStartupTimeout(time.Minute)),
		testcontainers.WithLogger(tclog.NewNoopLogger()),
	)
	if err != nil {
		if ctr != nil {
			_ = testcontainers.TerminateContainer(ctr)
		}
		return nil, fmt.Errorf("start fakellm container: %w", err)
	}
	ep, err := ctr.PortEndpoint(ctx, port, "http")
	if err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		return nil, fmt.Errorf("fakellm endpoint: %w", err)
	}
	return &Container{ctr: ctr, url: ep, hc: &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}}, nil
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("fakellmcontainer: no caller information")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("fakellmcontainer: go.mod not found above " + filepath.Dir(file))
		}
		dir = parent
	}
}

func (c *Container) URL() string { return c.url }

func (c *Container) BaseURL() string { return c.url + "/v1" }

func (c *Container) Terminate(ctx context.Context) error {
	c.hc.CloseIdleConnections()
	if err := c.ctr.Terminate(ctx); err != nil {
		return fmt.Errorf("terminate fakellm container: %w", err)
	}
	return nil
}

func (c *Container) Requests(ctx context.Context, model string) ([]fakellm.Record, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/_fake/requests", http.NoBody)
	if err != nil {
		return nil, 0, fmt.Errorf("fakellm requests: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fakellm requests: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("fakellm requests: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("fakellm requests: status %d", resp.StatusCode)
	}
	var out struct {
		Requests []fakellm.Record `json:"requests"`
		Dropped  int              `json:"dropped"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, 0, fmt.Errorf("fakellm requests: %w", err)
	}
	if model == "" {
		return out.Requests, out.Dropped, nil
	}
	var recs []fakellm.Record
	for _, r := range out.Requests {
		if r.Model == model {
			recs = append(recs, r)
		}
	}
	return recs, out.Dropped, nil
}

func RunTests(m *testing.M, set func(*Container)) int {
	tclog.SetDefault(tclog.NewNoopLogger())
	c, err := Start(context.Background())
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	set(c)
	code := m.Run()
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := c.Terminate(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	if code == 0 {
		if err := goleak.Find(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "goleak:", err)
			code = 1
		}
	}
	return code
}
