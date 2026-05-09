package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// SandboxState is the high-level lifecycle state of a sandbox.
type SandboxState string

const (
	StatePending    SandboxState = "Pending"
	StateRunning    SandboxState = "Running"
	StatePausing    SandboxState = "Pausing"
	StatePaused     SandboxState = "Paused"
	StateStopping   SandboxState = "Stopping"
	StateTerminated SandboxState = "Terminated"
	StateFailed     SandboxState = "Failed"
)

// SandboxStatus carries the lifecycle state plus optional reason/message.
type SandboxStatus struct {
	State            SandboxState `json:"state"`
	Reason           string       `json:"reason,omitempty"`
	Message          string       `json:"message,omitempty"`
	LastTransitionAt *time.Time   `json:"lastTransitionAt,omitempty"`
}

// ImageSpec describes the container image to use when creating a sandbox.
type ImageSpec struct {
	URI string `json:"uri"`
}

// PlatformSpec constrains the OS and CPU architecture for sandbox scheduling.
type PlatformSpec struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// ResourceLimits defines runtime resource constraints (cpu, memory, …).
type ResourceLimits map[string]string

// CreateSandboxRequest is the request body for POST /sandboxes.
type CreateSandboxRequest struct {
	Image          *ImageSpec        `json:"image,omitempty"`
	Platform       *PlatformSpec     `json:"platform,omitempty"`
	Timeout        *int              `json:"timeout,omitempty"`
	ResourceLimits ResourceLimits    `json:"resourceLimits"`
	Env            map[string]string `json:"env,omitempty"`
	Entrypoint     []string          `json:"entrypoint,omitempty"`
}

// SandboxInfo is the response from create/get sandbox calls.
type SandboxInfo struct {
	ID         string            `json:"id"`
	Status     SandboxStatus     `json:"status"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Entrypoint []string          `json:"entrypoint"`
	ExpiresAt  *time.Time        `json:"expiresAt,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
}

// ErrorResponse is the standard error body returned by the API for non-2xx responses.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// APIError wraps an ErrorResponse with the HTTP status code.
type APIError struct {
	StatusCode int
	Response   ErrorResponse
}

func (e *APIError) Error() string {
	return fmt.Sprintf("opensandbox API error %d: %s: %s", e.StatusCode, e.Response.Code, e.Response.Message)
}

// apiLifecycleClient implements lifecycleClient against the OpenSandbox HTTP API.
type apiLifecycleClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newAPILifecycleClient(serverURL, apiKey string) *apiLifecycleClient {
	return &apiLifecycleClient{
		baseURL:    serverURL + "/v1",
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

func (c *apiLifecycleClient) do(ctx context.Context, method, path string, body, result any) error {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("OPEN-SANDBOX-API-KEY", c.apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		data, _ := io.ReadAll(resp.Body)
		if jsonErr := json.Unmarshal(data, &apiErr.Response); jsonErr != nil || apiErr.Response.Code == "" {
			apiErr.Response = ErrorResponse{
				Code:    http.StatusText(resp.StatusCode),
				Message: string(data),
			}
		}
		return apiErr
	}

	if resp.StatusCode == http.StatusNoContent || result == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

func (c *apiLifecycleClient) CreateSandbox(ctx context.Context, req CreateSandboxRequest) (*SandboxInfo, error) {
	var info SandboxInfo
	if err := c.do(ctx, http.MethodPost, "/sandboxes", req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *apiLifecycleClient) GetSandbox(ctx context.Context, id string) (*SandboxInfo, error) {
	var info SandboxInfo
	if err := c.do(ctx, http.MethodGet, "/sandboxes/"+url.PathEscape(id), nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *apiLifecycleClient) DeleteSandbox(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/sandboxes/"+url.PathEscape(id), nil, nil)
}
