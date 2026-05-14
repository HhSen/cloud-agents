package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path"
	"time"

	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/internal/task"
	"gorm.io/gorm"
)

// DefaultAgentPort is the port the claude-agent-server listens on inside the sandbox.
// It must match the PORT env var passed to the container.
const DefaultAgentPort = 3000

type lifecycleClient interface {
	CreateSandbox(ctx context.Context, req CreateSandboxRequest) (*SandboxInfo, error)
	GetSandbox(ctx context.Context, id string) (*SandboxInfo, error)
	DeleteSandbox(ctx context.Context, id string) error
	RenewSandboxExpiration(ctx context.Context, id string, expiresAt time.Time) error
}

// healthChecker polls the claude-agent-server until it is ready to accept sessions.
type healthChecker interface {
	WaitForHealth(ctx context.Context, proxyBaseURL string, headers map[string]string) error
}

// ofsReader fetches raw bytes from OFS storage.
type ofsReader interface {
	GetObjectBytes(ctx context.Context, key string) ([]byte, error)
}

const (
	defaultMemoryLimit = "4Gi"
	defaultCPULimit    = "2000m"
)

type Manager struct {
	lc             lifecycleClient
	serverURL      string
	apiKey         string
	baseEnv        map[string]string
	sandboxImage   string
	platform       *PlatformSpec
	memoryLimit    string
	cpuLimit       string
	timeoutSeconds int
	agentPort      int
	healthChecker  healthChecker
	httpClient     *http.Client

	// optional: set via WithResources to enable skill/MCP injection at provision time.
	kindsRepo db.KindsRepository
	ofsReader ofsReader

	// optional: set via WithSSHKeys to enable SSH key injection at provision time.
	gormDB       *gorm.DB
	sshKeySecret string
}

const defaultTimeoutSeconds = 3600

func NewManager(serverURL, apiKey string, baseEnv map[string]string, image string, platform *PlatformSpec, memoryLimit, cpuLimit string, timeoutSeconds int) *Manager {
	if memoryLimit == "" {
		memoryLimit = defaultMemoryLimit
	}
	if cpuLimit == "" {
		cpuLimit = defaultCPULimit
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultTimeoutSeconds
	}
	return &Manager{
		lc:             newAPILifecycleClient(serverURL, apiKey),
		serverURL:      serverURL,
		apiKey:         apiKey,
		baseEnv:        baseEnv,
		sandboxImage:   image,
		platform:       platform,
		memoryLimit:    memoryLimit,
		cpuLimit:       cpuLimit,
		timeoutSeconds: timeoutSeconds,
		agentPort:      DefaultAgentPort,
		healthChecker:  newHTTPHealthChecker(&http.Client{Timeout: 5 * time.Second}),
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}
}

// WithResources enables skill and MCP injection at provision time.
// kr supplies the active resource records; reader fetches skill file bytes from OFS.
func (m *Manager) WithResources(kr db.KindsRepository, reader ofsReader) {
	m.kindsRepo = kr
	m.ofsReader = reader
}

// WithSSHKeys enables SSH key injection at provision time.
// gormDB is used to look up the user's encrypted key; sshKeySecret decrypts it.
func (m *Manager) WithSSHKeys(gormDB *gorm.DB, sshKeySecret string) {
	m.gormDB = gormDB
	m.sshKeySecret = sshKeySecret
}

// ProvisionForTask creates a sandbox and waits for it to be Running.
// It merges the manager's static baseEnv with per-task env vars from t,
// then injects SANDBOX_USER and TASK_ID so the entrypoint can set the CWD
// to /workspace/{username}/{task_id}/ and key OFS storage correctly.
func (m *Manager) ProvisionForTask(ctx context.Context, t *task.Task) error {
	env := make(map[string]string, len(m.baseEnv))
	for k, v := range m.baseEnv {
		env[k] = v
	}
	for k, v := range t.ExtraEnv() {
		env[k] = v
	}
	env["USERNAME"] = t.Username
	env["TASK_ID"] = t.ID

	timeout := m.timeoutSeconds
	info, err := m.lc.CreateSandbox(ctx, CreateSandboxRequest{
		Image:          &ImageSpec{URI: m.sandboxImage},
		Platform:       m.platform,
		Entrypoint:     []string{"/entrypoint.sh"},
		Timeout:        &timeout,
		ResourceLimits: ResourceLimits{"cpu": m.cpuLimit, "memory": m.memoryLimit},
		Env:            env,
	})
	if err != nil {
		return fmt.Errorf("create sandbox: %w", err)
	}

	sandboxID := info.ID
	slog.InfoContext(ctx, "sandbox created, waiting for Running", "sandboxID", sandboxID, "taskID", t.ID)

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

	if m.gormDB != nil && m.sshKeySecret != "" && t.Username != "" {
		if err := m.maybeInjectSSHKey(ctx, sandboxID, t.Username); err != nil {
			slog.WarnContext(ctx, "SSH key injection failed (continuing)", "taskID", t.ID, "error", err)
		}
	}

	// Clone before injecting resources: git clone requires an empty workspace directory,
	// but injectResources writes .claude/skills/... into it. Clone first, then overlay resources.
	if gitURL := t.GetGitURL(); gitURL != "" {
		taskCWD := fmt.Sprintf("/workspace/%s/%s", t.Username, t.ID)
		slog.InfoContext(ctx, "cloning repository", "taskID", t.ID, "gitURL", gitURL)
		if err := m.cloneRepo(ctx, sandboxID, gitURL, taskCWD); err != nil {
			return fmt.Errorf("clone repo: %w", err)
		}
	}

	if m.kindsRepo != nil && m.ofsReader != nil && t.UserID != 0 {
		if err := m.injectResources(ctx, t.UserID, t.Username, t.ID, sandboxID); err != nil {
			slog.WarnContext(ctx, "resource injection failed (continuing)", "taskID", t.ID, "error", err)
		}
	}

	t.SetRunning(sandboxID, proxyBaseURL, proxyHeaders)
	slog.InfoContext(ctx, "sandbox ready", "sandboxID", sandboxID, "proxyURL", proxyBaseURL)
	return nil
}

func (m *Manager) DeleteSandbox(ctx context.Context, sandboxID string) error {
	return m.lc.DeleteSandbox(ctx, sandboxID)
}

// RenewExpiration extends the sandbox TTL by m.timeoutSeconds from now.
func (m *Manager) RenewExpiration(ctx context.Context, sandboxID string) error {
	expiresAt := time.Now().Add(time.Duration(m.timeoutSeconds) * time.Second)
	return m.lc.RenewSandboxExpiration(ctx, sandboxID, expiresAt)
}

// injectResources fetches all active resources for userID and writes them into the
// sandbox via the execd file API before the agent session is created.
// Skills land at {taskCWD}/.claude/skills/{name}/SKILL.md (project source, auto-discovered).
// MCP servers are composed into {taskCWD}/.mcp.json.
func (m *Manager) injectResources(ctx context.Context, userID uint, username, taskID, sandboxID string) error {
	kinds, err := m.kindsRepo.ListActive(ctx, userID)
	if err != nil {
		return fmt.Errorf("list active resources: %w", err)
	}
	if len(kinds) == 0 {
		return nil
	}
	slog.InfoContext(ctx, "injecting resources", "count", len(kinds), "userID", userID, "sandboxID", sandboxID)

	taskCWD := fmt.Sprintf("/workspace/%s/%s", username, taskID)

	type mcpConfig struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	mcp := mcpConfig{MCPServers: make(map[string]json.RawMessage)}

	skillsDir := taskCWD + "/.claude/skills"

	for _, k := range kinds {
		switch k.Kind {
		case "skill":
			skillNameDir := skillsDir + "/" + k.Name
			skillFiles := k.SkillFiles()
			for _, relPath := range skillFiles {
				targetDir := skillNameDir
				if sub := path.Dir(relPath); sub != "." {
					targetDir = skillNameDir + "/" + sub
				}
				if err := m.makeDirAll(ctx, sandboxID, targetDir); err != nil {
					return fmt.Errorf("create dir for %q in skill %q: %w", relPath, k.Name, err)
				}
				content, err := m.ofsReader.GetObjectBytes(ctx, k.OFSPath+relPath)
				if err != nil {
					return fmt.Errorf("fetch %q from skill %q: %w", relPath, k.Name, err)
				}
				if err := m.writeFile(ctx, sandboxID, skillNameDir+"/"+relPath, content); err != nil {
					return fmt.Errorf("write %q in skill %q: %w", relPath, k.Name, err)
				}
			}
			slog.InfoContext(ctx, "injected skill", "name", k.Name, "files", len(skillFiles), "sandboxID", sandboxID)
		case "mcp":
			mcp.MCPServers[k.Name] = k.Meta
		}
	}

	if len(mcp.MCPServers) > 0 {
		data, err := json.Marshal(mcp)
		if err != nil {
			return fmt.Errorf("marshal mcp config: %w", err)
		}
		mcpPath := taskCWD + "/.mcp.json"
		if err := m.writeFile(ctx, sandboxID, mcpPath, data); err != nil {
			return fmt.Errorf("write .mcp.json to sandbox: %w", err)
		}
		slog.InfoContext(ctx, "injected MCP servers", "count", len(mcp.MCPServers), "sandboxID", sandboxID)
	}

	return nil
}

// fileMetadata is the per-file metadata entry for the POST /files/upload body.
type fileMetadata struct {
	Path string `json:"path"`
	Mode int    `json:"mode"`
}

// writeFile uploads content to absPath inside the sandbox with default mode 755.
func (m *Manager) writeFile(ctx context.Context, sandboxID, absPath string, content []byte) error {
	return m.writeFileWithMode(ctx, sandboxID, absPath, content, 755)
}

// writeFileWithMode uploads content to absPath with the given Unix mode.
func (m *Manager) writeFileWithMode(ctx context.Context, sandboxID, absPath string, content []byte, mode int) error {
	apiURL := fmt.Sprintf("%s/sandboxes/%s/proxy/44772/files/upload", m.serverURL, sandboxID)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	metaField, err := mw.CreateFormFile("metadata", "metadata.json")
	if err != nil {
		return fmt.Errorf("build execd upload metadata field: %w", err)
	}
	if err := json.NewEncoder(metaField).Encode(fileMetadata{Path: absPath, Mode: mode}); err != nil {
		return fmt.Errorf("encode execd upload metadata: %w", err)
	}

	fileField, err := mw.CreateFormFile("file", path.Base(absPath))
	if err != nil {
		return fmt.Errorf("build execd upload file field: %w", err)
	}
	if _, err := fileField.Write(content); err != nil {
		return fmt.Errorf("write execd upload file content: %w", err)
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, &buf)
	if err != nil {
		return fmt.Errorf("build execd upload request: %w", err)
	}
	req.Header.Set("X-OPEN-SANDBOX-API-KEY", m.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execd upload %s: %w", absPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("execd upload %s: status %d: %s", absPath, resp.StatusCode, body)
	}
	return nil
}

// dirPermission is the per-path permission entry for the POST /directories body.
type dirPermission struct {
	Mode int `json:"mode"`
}

// makeDirAll creates absPath and all missing parent directories inside the sandbox
// via the execd POST /directories API (mkdir -p semantics) with default mode 755.
func (m *Manager) makeDirAll(ctx context.Context, sandboxID, absPath string) error {
	return m.makeDirWithMode(ctx, sandboxID, absPath, 755)
}

// makeDirWithMode creates absPath (and parent directories) with the given Unix mode.
func (m *Manager) makeDirWithMode(ctx context.Context, sandboxID, absPath string, mode int) error {
	apiURL := fmt.Sprintf("%s/sandboxes/%s/proxy/44772/directories", m.serverURL, sandboxID)

	payload, err := json.Marshal(map[string]dirPermission{absPath: {Mode: mode}})
	if err != nil {
		return fmt.Errorf("marshal mkdir request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build execd mkdir request: %w", err)
	}
	req.Header.Set("X-OPEN-SANDBOX-API-KEY", m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execd mkdir %s: %w", absPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("execd mkdir %s: status %d: %s", absPath, resp.StatusCode, body)
	}
	return nil
}

// IsSandboxAlive reports whether sandboxID is still in Running state.
// A 404 response is treated as (false, nil) — the sandbox was cleaned up by the server.
func (m *Manager) IsSandboxAlive(ctx context.Context, sandboxID string) (bool, error) {
	info, err := m.lc.GetSandbox(ctx, sandboxID)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return info.Status.State == StateRunning, nil
}

// httpHealthChecker polls GET {proxyBaseURL}/health until the claude-agent-server
// reports healthy.
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

	slog.InfoContext(pollCtx, "waiting for agent server health", "url", healthURL)

	for {
		if pollCtx.Err() != nil {
			return fmt.Errorf("timed out waiting for agent server health: %w", pollCtx.Err())
		}

		healthy, err := h.check(pollCtx, healthURL, headers)
		if err != nil {
			var statusErr *httpStatusError
			if errors.As(err, &statusErr) && (statusErr.code == http.StatusUnauthorized || statusErr.code == http.StatusForbidden) {
				return err
			}
			slog.WarnContext(pollCtx, "agent server health check error, retrying", "error", err)
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
