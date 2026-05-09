package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/your-org/platform-backend/internal/conversation"
)

// DefaultAgentPort is the port the claude-agent-server listens on inside the sandbox.
// It must match the PORT env var passed to the container.
const DefaultAgentPort = 3000

type lifecycleClient interface {
	CreateSandbox(ctx context.Context, req CreateSandboxRequest) (*SandboxInfo, error)
	GetSandbox(ctx context.Context, id string) (*SandboxInfo, error)
	DeleteSandbox(ctx context.Context, id string) error
}

// healthChecker polls the claude-agent-server until it is ready to accept sessions.
type healthChecker interface {
	WaitForHealth(ctx context.Context, proxyBaseURL string, headers map[string]string) error
}

type Manager struct {
	lc            lifecycleClient
	serverURL     string
	apiKey        string
	baseEnv       map[string]string
	sandboxImage  string
	platform      *PlatformSpec
	agentPort     int
	healthChecker healthChecker
}

func NewManager(serverURL, apiKey string, baseEnv map[string]string, image string, platform *PlatformSpec) *Manager {
	return &Manager{
		lc:            newAPILifecycleClient(serverURL, apiKey),
		serverURL:     serverURL,
		apiKey:        apiKey,
		baseEnv:       baseEnv,
		sandboxImage:  image,
		platform:      platform,
		agentPort:     DefaultAgentPort,
		healthChecker: newHTTPHealthChecker(&http.Client{Timeout: 5 * time.Second}),
	}
}

// ProvisionForConversation creates a sandbox and waits for it to be Running.
// It merges the manager's static baseEnv with per-conversation env vars from conv.
func (m *Manager) ProvisionForConversation(ctx context.Context, conv *conversation.Conversation) error {
	env := make(map[string]string, len(m.baseEnv))
	for k, v := range m.baseEnv {
		env[k] = v
	}
	for k, v := range conv.ExtraEnv() {
		env[k] = v
	}

	timeout := 3600
	info, err := m.lc.CreateSandbox(ctx, CreateSandboxRequest{
		Image:          &ImageSpec{URI: m.sandboxImage},
		Platform:       m.platform,
		Entrypoint:     []string{"/entrypoint.sh"},
		Timeout:        &timeout,
		ResourceLimits: ResourceLimits{"cpu": "500m", "memory": "512Mi"},
		Env:            env,
	})
	if err != nil {
		return fmt.Errorf("create sandbox: %w", err)
	}

	sandboxID := info.ID
	log.Printf("sandbox %s created, waiting for Running state", sandboxID)

	pollCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	for {
		if pollCtx.Err() != nil {
			return fmt.Errorf("sandbox %s did not reach Running: %w", sandboxID, pollCtx.Err())
		}

		current, err := m.lc.GetSandbox(pollCtx, sandboxID)
		if err != nil {
			return fmt.Errorf("poll sandbox %s: %w", sandboxID, err)
		}

		switch current.Status.State {
		case StateRunning:
			// proceed
		case StateFailed, StateTerminated:
			return fmt.Errorf("sandbox %s entered terminal state: %s (%s)",
				sandboxID, current.Status.State, current.Status.Reason)
		default:
			select {
			case <-pollCtx.Done():
				return fmt.Errorf("sandbox %s did not reach Running: %w", sandboxID, pollCtx.Err())
			case <-time.After(2 * time.Second):
			}
			continue
		}
		break
	}

	proxyBaseURL := fmt.Sprintf("%s/sandboxes/%s/proxy/%d", m.serverURL, sandboxID, m.agentPort)
	proxyHeaders := map[string]string{}
	if m.apiKey != "" {
		proxyHeaders["Authorization"] = "Bearer " + m.apiKey
	}

	if err := m.healthChecker.WaitForHealth(ctx, proxyBaseURL, proxyHeaders); err != nil {
		return fmt.Errorf("sandbox %s agent server not ready: %w", sandboxID, err)
	}

	conv.SetRunning(sandboxID, proxyBaseURL, proxyHeaders)
	log.Printf("sandbox %s ready — proxy URL: %s", sandboxID, proxyBaseURL)
	return nil
}

func (m *Manager) DeleteSandbox(ctx context.Context, sandboxID string) error {
	return m.lc.DeleteSandbox(ctx, sandboxID)
}

// httpHealthChecker polls GET {proxyBaseURL}/health until the claude-agent-server
// reports healthy. The container may be Running while the server process is still
// starting, so this check is required before sending any sessions.
type httpHealthChecker struct {
	client *http.Client
}

func newHTTPHealthChecker(client *http.Client) *httpHealthChecker {
	return &httpHealthChecker{client: client}
}

func (h *httpHealthChecker) WaitForHealth(ctx context.Context, proxyBaseURL string, headers map[string]string) error {
	healthURL := proxyBaseURL + "/health"

	pollCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	log.Printf("waiting for agent server health at %s", healthURL)

	for {
		if pollCtx.Err() != nil {
			return fmt.Errorf("timed out waiting for agent server health: %w", pollCtx.Err())
		}

		healthy, err := h.check(pollCtx, healthURL, headers)
		if err != nil {
			var statusErr *httpStatusError
			if errors.As(err, &statusErr) && (statusErr.code == http.StatusUnauthorized || statusErr.code == http.StatusForbidden) {
				return err // auth errors won't resolve on retry
			}
			log.Printf("agent server health check error (retrying): %v", err)
		} else if healthy {
			return nil
		}

		select {
		case <-pollCtx.Done():
			return fmt.Errorf("timed out waiting for agent server health: %w", pollCtx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("health endpoint returned %d", e.code)
}

func (h *httpHealthChecker) check(ctx context.Context, url string, headers map[string]string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, &httpStatusError{code: resp.StatusCode}
	}

	var payload struct {
		Healthy bool `json:"healthy"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload); err != nil {
		return false, fmt.Errorf("decode health response: %w", err)
	}
	return payload.Healthy, nil
}
