package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/your-org/platform-backend/internal/auth"
	"github.com/your-org/platform-backend/internal/db"
	"github.com/your-org/platform-backend/internal/storage"
	"github.com/your-org/platform-backend/internal/task"
	"github.com/your-org/platform-backend/pkg/config"
	"github.com/your-org/platform-backend/pkg/logger"
	"gorm.io/gorm"
)

var validResourceName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

const (
	maxSkillFiles    = 20
	maxSkillFileSize = 1 << 20 // 1 MiB
)

var validSkillFilePath = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)

// isValidSkillFilePath checks that p is a safe relative path (no traversal, no empty segments).
func isValidSkillFilePath(p string) bool {
	if p == "" || !validSkillFilePath.MatchString(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return false
		}
	}
	return true
}

// TaskStore is the storage interface for Task records.
type TaskStore = task.Repository

// SandboxManager provisions and tears down the compute sandbox that backs a task.
type SandboxManager interface {
	// ProvisionForTask allocates a sandbox for t and attaches its ID to t.
	ProvisionForTask(ctx context.Context, t *task.Task) error
	// DeleteSandbox destroys the sandbox identified by sandboxID.
	DeleteSandbox(ctx context.Context, sandboxID string) error
	// IsSandboxAlive reports whether sandboxID is still in Running state.
	// Returns (false, nil) when the sandbox has expired or been terminated.
	IsSandboxAlive(ctx context.Context, sandboxID string) (bool, error)
}

// FileStore retrieves and manages task history in OFS-backed file storage.
type FileStore interface {
	ListHistory(ctx context.Context, username, taskID string) ([]string, error)
	GetHistory(ctx context.Context, key string) ([]json.RawMessage, error)
	GetSessionMeta(ctx context.Context, username, taskID string) (*storage.SessionMeta, error)
	DeleteHistory(ctx context.Context, username, taskID string) error
}

// ResourceWriter writes files to OFS storage.
type ResourceWriter interface {
	PutObject(ctx context.Context, key string, data []byte) error
}

// ResourceReader reads files from OFS storage.
type ResourceReader interface {
	GetObjectBytes(ctx context.Context, key string) ([]byte, error)
}

// WorkspaceReader reads workspace files from OFS backing storage.
type WorkspaceReader interface {
	ListWorkspace(ctx context.Context, username, taskID, subpath string) ([]storage.WorkspaceEntry, error)
	GetWorkspaceFile(ctx context.Context, username, taskID, filePath string) ([]byte, error)
}

// MessageProxy streams a prompt from the client through to the task's sandbox.
type MessageProxy interface {
	// StreamMessage forwards prompt to the sandbox associated with t and writes
	// the streamed response directly to w.
	StreamMessage(ctx context.Context, t *task.Task, prompt string, w http.ResponseWriter) error
	// RespondToPermission forwards a permission decision (allow/deny) to the
	// sandbox for the pending canUseTool request on the session.
	RespondToPermission(ctx context.Context, t *task.Task, decision string) error
	// RespondToQuestion forwards user answers to a pending AskUserQuestion request.
	RespondToQuestion(ctx context.Context, t *task.Task, answers map[string]any) error
}

// Handler wires together the store, sandbox manager, message proxy, and file store
// to serve the tasks REST API.
type Handler struct {
	store           TaskStore
	manager         SandboxManager
	proxy           MessageProxy
	fileStore       FileStore
	kindsRepo       db.KindsRepository // optional; nil disables resource API
	ofsWriter       ResourceWriter     // optional; nil disables resource content upload
	ofsReader       ResourceReader     // optional; nil disables resource content read
	workspaceReader WorkspaceReader    // optional; nil disables OFS workspace browsing
	serverURL       string             // sandbox lifecycle server base URL
	sandboxAPIKey   string             // X-OPEN-SANDBOX-API-KEY value
	httpClient      *http.Client       // reused for execd proxy requests
}

// NewHandler constructs a Handler from its dependencies.
func NewHandler(store TaskStore, mgr SandboxManager, proxy MessageProxy, fileStore FileStore) *Handler {
	return &Handler{
		store:      store,
		manager:    mgr,
		proxy:      proxy,
		fileStore:  fileStore,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *Handler) withExecd(serverURL, apiKey string) {
	h.serverURL = serverURL
	h.sandboxAPIKey = apiKey
}

func (h *Handler) withResources(kr db.KindsRepository, w ResourceWriter, r ResourceReader) {
	h.kindsRepo = kr
	h.ofsWriter = w
	h.ofsReader = r
}

func (h *Handler) withWorkspace(r WorkspaceReader) {
	h.workspaceReader = r
}

// PasswordLoginHandler returns a Gin handler for POST /api/auth/login.
//
// @Summary      Password login
// @Description  Authenticate with username + password and receive an access token.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body      passwordLoginRequest  true  "Login credentials"
// @Success      200   {object}  tokenResponse
// @Failure      400   {object}  errorResponse  "username and password required"
// @Failure      401   {object}  errorResponse  "invalid credentials"
// @Failure      500   {object}  errorResponse  "internal error"
// @Router       /api/auth/login [post]
func PasswordLoginHandler(gormDB *gorm.DB, authCfg config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body passwordLoginRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "username and password required"})
			return
		}
		user, err := db.FindByCredentials(gormDB, body.Username, body.Password)
		if err != nil {
			logger.Default().Error("password login db error", "err", err)
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal error"})
			return
		}
		if user == nil {
			c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid credentials"})
			return
		}
		ttl := time.Duration(authCfg.TokenTTLSeconds) * time.Second
		token, err := auth.CreateToken(authCfg.SecretKey, ttl, user.ID, user.UserName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to issue token"})
			return
		}
		c.JSON(http.StatusOK, tokenResponse{AccessToken: token})
	}
}

// RegisterHandler handles POST /api/auth/register (username + password + email).
//
// @Summary      Register a user
// @Description  Create a new local user account and receive an access token.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body      registerRequest  true  "Registration request"
// @Success      201   {object}  tokenResponse
// @Failure      400   {object}  errorResponse  "username and password required"
// @Failure      409   {object}  errorResponse  "username already taken"
// @Failure      500   {object}  errorResponse  "failed to issue token"
// @Router       /api/auth/register [post]
func RegisterHandler(gormDB *gorm.DB, authCfg config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body registerRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "username and password required"})
			return
		}
		if body.Email == "" {
			body.Email = body.Username + "@local"
		}
		user, err := db.CreateWithPassword(gormDB, body.Username, body.Email, body.Password)
		if err != nil {
			logger.Default().Error("register user", "err", err)
			c.JSON(http.StatusConflict, errorResponse{Error: "username already taken"})
			return
		}
		ttl := time.Duration(authCfg.TokenTTLSeconds) * time.Second
		token, err := auth.CreateToken(authCfg.SecretKey, ttl, user.ID, user.UserName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to issue token"})
			return
		}
		c.JSON(http.StatusCreated, tokenResponse{AccessToken: token})
	}
}

// checkTaskOwner returns true and writes a 403 if the authenticated user does not
// own the task. When auth is disabled (no user on context) ownership is not enforced.
func checkTaskOwner(c *gin.Context, taskUsername string) bool {
	u := auth.GetUser(c)
	if u == nil {
		return false
	}
	if u.UserName != taskUsername {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return true
	}
	return false
}

// CreateTask handles POST /api/tasks.
//
// @Summary      Create a task
// @Description  Create a new task with an optional username and environment variables.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        body  body      createTaskRequest   true  "Create task request"
// @Success      201   {object}  createTaskResponse
// @Failure      400   {string}  string  "invalid request body"
// @Failure      500   {string}  string  "failed to create task"
// @Router       /api/tasks [post]
func (h *Handler) CreateTask(c *gin.Context) {
	var body createTaskRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.String(http.StatusBadRequest, "invalid request body")
		return
	}

	// If auth middleware is active, override the body username with the authenticated user.
	if u := auth.GetUser(c); u != nil {
		body.Username = u.UserName
	}

	t, err := h.store.Create(c.Request.Context(), body.Username, body.Env)
	if err != nil {
		logger.Default().Error("create task", "err", err)
		c.String(http.StatusInternalServerError, "failed to create task")
		return
	}
	c.JSON(http.StatusCreated, createTaskResponse{ID: t.ID})
}

// SendMessage handles POST /api/tasks/:id/messages.
//
// Lazily provisions the task's sandbox on first use, then streams the
// assistant response back to the caller. Provisioning runs under a background
// context so that a client disconnect does not abort it.
//
// @Summary      Send a message to a task
// @Description  Lazily provisions the task sandbox on first use and streams the assistant response back to the caller.
// @Tags         tasks
// @Accept       json
// @Produce      plain
// @Param        id    path      string             true  "Task ID"
// @Param        body  body      sendMessageRequest true  "Send message request"
// @Success      200   {string}  string             "Streamed assistant response"
// @Failure      400   {string}  string             "prompt is required"
// @Failure      404   {string}  string             "task not found"
// @Failure      500   {string}  string             "failed to get task"
// @Failure      502   {string}  string             "failed to provision sandbox"
// @Router       /api/tasks/{id}/messages [post]
func (h *Handler) SendMessage(c *gin.Context) {
	id := c.Param("id")
	log := logger.Default().With("task_id", id)
	t, err := h.store.Get(c.Request.Context(), id)
	if err != nil {
		log.Error("get task", "err", err)
		c.String(http.StatusInternalServerError, "failed to get task")
		return
	}
	if t == nil {
		c.String(http.StatusNotFound, "task not found")
		return
	}
	if checkTaskOwner(c, t.Username) {
		return
	}

	var body sendMessageRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.String(http.StatusBadRequest, "prompt is required")
		return
	}

	// Sandboxes expire silently via TTL. ResetIfExpired checks liveness under the
	// provisioning lock so a concurrent re-provision cannot be stomped by a racing reset.
	if err := t.ResetIfExpired(func(sandboxID string) (bool, error) {
		aliveCtx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		alive, err := h.manager.IsSandboxAlive(aliveCtx, sandboxID)
		if err != nil {
			log.Error("sandbox status check failed", "sandbox_id", sandboxID, "err", err)
			return true, err // treat check error as alive — proxy will surface real errors
		}
		if !alive {
			log.Info("sandbox expired, re-provisioning", "sandbox_id", sandboxID)
		}
		return alive, nil
	}); err != nil {
		log.Warn("sandbox liveness check error, proceeding", "err", err)
	}

	if t.GetState() == task.StateNew {
		t.SetProvisioning()
	}

	// Use background context so provisioning survives client disconnects.
	provisionCtx := context.Background()
	err = t.EnsureProvisioned(func() error {
		return h.manager.ProvisionForTask(provisionCtx, t)
	})
	if err != nil {
		t.SetError()
		log.Error("provision failed", "err", err)
		c.String(http.StatusBadGateway, "failed to provision sandbox")
		return
	}

	if err := h.proxy.StreamMessage(c.Request.Context(), t, body.Prompt, c.Writer); err != nil {
		if c.Request.Context().Err() != nil {
			return // client disconnected
		}
		log.Error("stream error", "err", err)
	}
}

// GetTask handles GET /api/tasks/:id.
//
// @Summary      Get a task
// @Description  Retrieve task state by ID.
// @Tags         tasks
// @Produce      json
// @Param        id   path      string          true  "Task ID"
// @Success      200  {object}  getTaskResponse
// @Failure      404  {string}  string          "task not found"
// @Failure      500  {string}  string          "failed to get task"
// @Router       /api/tasks/{id} [get]
func (h *Handler) GetTask(c *gin.Context) {
	id := c.Param("id")
	log := logger.Default().With("task_id", id)
	t, err := h.store.Get(c.Request.Context(), id)
	if err != nil {
		log.Error("get task", "err", err)
		c.String(http.StatusInternalServerError, "failed to get task")
		return
	}
	if t == nil {
		c.String(http.StatusNotFound, "task not found")
		return
	}
	if checkTaskOwner(c, t.Username) {
		return
	}

	_, sandboxID, sessionID, stateStr := t.Info()
	c.JSON(http.StatusOK, getTaskResponse{
		ID:        id,
		Username:  t.Username,
		State:     stateStr,
		SandboxID: sandboxID,
		SessionID: sessionID,
		Title:     t.GetTitle(),
		CWD:       fmt.Sprintf("/workspace/%s/%s", t.Username, id),
	})
}

// DeleteTask handles DELETE /api/tasks/:id.
//
// @Summary      Delete a task
// @Description  Delete a task and its associated sandbox.
// @Tags         tasks
// @Param        id   path  string  true  "Task ID"
// @Success      204
// @Failure      404  {string}  string  "task not found"
// @Failure      500  {string}  string  "failed to delete task"
// @Router       /api/tasks/{id} [delete]
func (h *Handler) DeleteTask(c *gin.Context) {
	id := c.Param("id")
	log := logger.Default().With("task_id", id)
	t, err := h.store.Get(c.Request.Context(), id)
	if err != nil {
		log.Error("get task", "err", err)
		c.String(http.StatusInternalServerError, "failed to get task")
		return
	}
	if t == nil {
		c.String(http.StatusNotFound, "task not found")
		return
	}
	if checkTaskOwner(c, t.Username) {
		return
	}

	sandboxID := t.GetSandboxID()
	username := t.Username
	if err := h.store.Delete(c.Request.Context(), id); err != nil {
		log.Error("delete task", "err", err)
		c.String(http.StatusInternalServerError, "failed to delete task")
		return
	}

	if sandboxID != "" {
		if err := h.manager.DeleteSandbox(context.Background(), sandboxID); err != nil {
			log.Warn("delete sandbox failed", "sandbox_id", sandboxID, "err", err)
		}
	}

	if h.fileStore != nil {
		if err := h.fileStore.DeleteHistory(context.Background(), username, id); err != nil {
			log.Warn("delete history for task %s: %v", id, err)
		}
	}

	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}

// GetTaskHistory handles GET /api/tasks/:id/history.
//
// @Summary      Get task conversation history
// @Description  Retrieve the conversation history for a task. Requires fileStore to be configured.
// @Tags         tasks
// @Produce      json
// @Param        id   path      string  true  "Task ID"
// @Success      200  {array}   object  "Raw session entries (see @anthropic-ai/claude-agent-sdk SDKMessage)"
// @Failure      404  {string}  string  "task not found"
// @Failure      500  {string}  string  "failed to get history"
// @Failure      503  {string}  string  "history storage not configured"
// @Router       /api/tasks/{id}/history [get]
func (h *Handler) GetTaskHistory(c *gin.Context) {
	id := c.Param("id")
	log := logger.Default().With("task_id", id, "handler", "GetTaskHistory")

	t, err := h.store.Get(c.Request.Context(), id)
	if err != nil {
		log.Error("failed to get task", "err", err)
		c.String(http.StatusInternalServerError, "failed to get task")
		return
	}
	if t == nil {
		log.Warn("task not found")
		c.String(http.StatusNotFound, "task not found")
		return
	}
	if checkTaskOwner(c, t.Username) {
		return
	}

	if h.fileStore == nil {
		log.Warn("history storage not configured")
		c.String(http.StatusServiceUnavailable, "history storage not configured")
		return
	}

	log.Info("listing history sessions", "username", t.Username)
	keys, err := h.fileStore.ListHistory(c.Request.Context(), t.Username, id)
	if err != nil {
		log.Error("failed to list history sessions", "err", err)
		c.String(http.StatusInternalServerError, "failed to list history")
		return
	}
	log.Info("found sessions", "session_count", len(keys))

	entries := make([]json.RawMessage, 0)
	for _, key := range keys {
		log.Info("reading session parts", "session_prefix", key)
		part, err := h.fileStore.GetHistory(c.Request.Context(), key)
		if err != nil {
			log.Error("failed to read session parts", "session_prefix", key, "err", err)
			c.String(http.StatusInternalServerError, "failed to get history")
			return
		}
		log.Info("read session parts", "session_prefix", key, "entry_count", len(part))
		entries = append(entries, part...)
	}

	log.Info("returning history", "total_entries", len(entries))
	c.JSON(http.StatusOK, entries)
}

// RespondToPermission handles POST /api/tasks/:id/permissions.
//
// @Summary      Respond to a pending tool permission request
// @Description  Approve or deny a canUseTool permission request that has paused the agent session.
// @Tags         tasks
// @Accept       json
// @Param        id    path  string                          true  "Task ID"
// @Param        body  body  respondToPermissionRequest      true  "Permission decision"
// @Success      204
// @Failure      400  {string}  string  "invalid request body"
// @Failure      404  {string}  string  "task not found or no pending permission"
// @Failure      502  {string}  string  "failed to respond to permission"
// @Router       /api/tasks/{id}/permissions [post]
func (h *Handler) RespondToPermission(c *gin.Context) {
	id := c.Param("id")
	log := logger.Default().With("task_id", id)
	t, err := h.store.Get(c.Request.Context(), id)
	if err != nil {
		log.Error("get task", "err", err)
		c.String(http.StatusInternalServerError, "failed to get task")
		return
	}
	if t == nil {
		c.String(http.StatusNotFound, "task not found")
		return
	}

	var body respondToPermissionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.String(http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.proxy.RespondToPermission(c.Request.Context(), t, body.Decision); err != nil {
		log.Error("respond to permission", "err", err)
		c.String(http.StatusBadGateway, "failed to respond to permission")
		return
	}

	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}

// RespondToQuestion handles POST /api/tasks/:id/questions.
//
// @Summary      Respond to a pending AskUserQuestion request
// @Description  Submit answers to a clarifying question that has paused the agent session.
// @Tags         tasks
// @Accept       json
// @Param        id    path  string                         true  "Task ID"
// @Param        body  body  respondToQuestionRequest       true  "Question answers"
// @Success      204
// @Failure      400  {string}  string  "invalid request body"
// @Failure      404  {string}  string  "task not found or no pending question"
// @Failure      502  {string}  string  "failed to respond to question"
// @Router       /api/tasks/{id}/questions [post]
func (h *Handler) RespondToQuestion(c *gin.Context) {
	id := c.Param("id")
	log := logger.Default().With("task_id", id)
	t, err := h.store.Get(c.Request.Context(), id)
	if err != nil {
		log.Error("get task", "err", err)
		c.String(http.StatusInternalServerError, "failed to get task")
		return
	}
	if t == nil {
		c.String(http.StatusNotFound, "task not found")
		return
	}

	var body respondToQuestionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.String(http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.proxy.RespondToQuestion(c.Request.Context(), t, body.Answers); err != nil {
		log.Error("respond to question", "err", err)
		c.String(http.StatusBadGateway, "failed to respond to question")
		return
	}

	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}

// ListTasks handles GET /api/tasks.
//
// @Summary      List tasks for the authenticated user
// @Description  Returns task summaries ordered by most recently updated.
// @Tags         tasks
// @Produce      json
// @Success      200  {array}   taskListItem
// @Router       /api/tasks [get]
func (h *Handler) ListTasks(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusOK, []taskListItem{})
		return
	}
	tasks, err := h.store.List(c.Request.Context(), u.UserName)
	if err != nil {
		logger.Default().Error("list tasks", "username", u.UserName, "err", err)
		c.String(http.StatusInternalServerError, "failed to list tasks")
		return
	}
	items := make([]taskListItem, len(tasks))
	for i, t := range tasks {
		items[i] = taskListItem{
			ID:        t.ID,
			Title:     t.Title,
			State:     t.State,
			CreatedAt: t.CreatedAt,
			UpdatedAt: t.UpdatedAt,
		}
	}
	c.JSON(http.StatusOK, items)
}

// Health handles GET /health.
//
// @Summary      Health check
// @Description  Liveness probe — returns ok when the server is up.
// @Tags         health
// @Produce      json
// @Success      200  {object}  healthResponse
// @Router       /health [get]
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{Status: "ok"})
}

// CreateResource handles POST /api/resources.
//
// @Summary      Create a resource
// @Description  Create a skill (SKILL.md) or mcp (JSON config) resource owned by the authenticated user.
// @Tags         resources
// @Accept       json
// @Produce      json
// @Param        body  body      createResourceRequest  true  "Resource definition"
// @Success      201   {object}  resourceResponse
// @Failure      400   {object}  errorResponse  "invalid request body"
// @Failure      401   {object}  errorResponse  "unauthorized"
// @Failure      500   {object}  errorResponse  "failed to store resource"
// @Failure      503   {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources [post]
func (h *Handler) CreateResource(c *gin.Context) {
	if h.kindsRepo == nil || h.ofsWriter == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var body createResourceRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if body.Kind != "skill" && body.Kind != "mcp" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be 'skill' or 'mcp'"})
		return
	}
	if !validResourceName.MatchString(body.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must match ^[a-zA-Z0-9_-]+$"})
		return
	}

	var ofsPath string
	var meta json.RawMessage
	var ofsKey string
	var ofsContent []byte

	switch body.Kind {
	case "skill":
		if body.Content == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "content (SKILL.md) is required for skill resources"})
			return
		}
		ofsPath = fmt.Sprintf("%s/resources/skills/%s/", u.UserName, body.Name)
		ofsKey = ofsPath + "SKILL.md"
		ofsContent = []byte(body.Content)
		initMeta, _ := json.Marshal(db.SkillMeta{Files: []string{"SKILL.md"}})
		meta = initMeta
	case "mcp":
		ofsPath = fmt.Sprintf("%s/resources/mcp/%s.json", u.UserName, body.Name)
		ofsKey = ofsPath
		raw := body.Meta
		if len(raw) == 0 {
			raw = json.RawMessage(body.Content)
		}
		if !json.Valid(raw) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "mcp meta must be valid JSON"})
			return
		}
		meta = raw
		ofsContent = []byte(meta)
	}

	if err := h.ofsWriter.PutObject(c.Request.Context(), ofsKey, ofsContent); err != nil {
		logger.Default().Error("put resource to OFS", "key", ofsKey, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store resource"})
		return
	}

	rec, err := h.kindsRepo.Create(c.Request.Context(), u.ID, body.Kind, body.Name, ofsPath, meta)
	if err != nil {
		logger.Default().Error("create kind record", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create resource"})
		return
	}

	c.JSON(http.StatusCreated, kindRecordToResponse(rec))
}

// CreateSkillFromZip handles POST /api/resources/zip.
// Accepts a multipart form with fields "name" and "file" (a .zip archive).
// The zip must contain SKILL.md at its root; all other files are uploaded as
// companion files. Validates path safety, per-file size (1 MiB), and total
// file count (20 max) before writing anything to OFS.
//
// @Summary      Create a skill from a zip archive
// @Description  Create a multi-file skill by uploading a zip. The zip must contain SKILL.md at root. Other files are stored as companion files (max 20 total, 1 MiB each).
// @Tags         resources
// @Accept       multipart/form-data
// @Produce      json
// @Param        name  formData  string  true  "Skill name ([a-zA-Z0-9_-]+)"
// @Param        file  formData  file    true  "Zip archive"
// @Success      201   {object}  resourceResponse
// @Failure      400   {object}  errorResponse  "invalid name, missing SKILL.md, or bad zip"
// @Failure      401   {object}  errorResponse  "unauthorized"
// @Failure      413   {object}  errorResponse  "zip or file too large"
// @Failure      422   {object}  errorResponse  "too many files"
// @Failure      500   {object}  errorResponse  "storage error"
// @Failure      503   {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/zip [post]
func (h *Handler) CreateSkillFromZip(c *gin.Context) {
	if h.kindsRepo == nil || h.ofsWriter == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	name := c.PostForm("name")
	if !validResourceName.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must match ^[a-zA-Z0-9_-]+$"})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}

	const maxZipSize = maxSkillFiles * maxSkillFileSize
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload"})
		return
	}
	defer f.Close()

	zipBytes, err := io.ReadAll(io.LimitReader(f, maxZipSize+1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload"})
		return
	}
	if len(zipBytes) > maxZipSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("zip exceeds %d MiB total limit", maxZipSize>>20)})
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid zip file"})
		return
	}

	type zipEntry struct {
		path    string
		content []byte
	}
	// Collect all non-directory file entries, filtering macOS metadata folders.
	var rawEntries []zipEntry
	for _, zf := range zr.File {
		// Use FileInfo().IsDir() (checks mode bits) rather than a name-suffix
		// check — more reliable across zip implementations.
		if zf.FileInfo().IsDir() {
			continue
		}
		// Strip macOS __MACOSX resource-fork entries.
		if strings.HasPrefix(zf.Name, "__MACOSX/") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read zip entry"})
			return
		}
		content, readErr := io.ReadAll(io.LimitReader(rc, maxSkillFileSize+1))
		rc.Close()
		if readErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read zip entry"})
			return
		}
		if len(content) > maxSkillFileSize {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file %q exceeds 1 MiB limit", zf.Name)})
			return
		}
		rawEntries = append(rawEntries, zipEntry{path: zf.Name, content: content})
	}

	if len(rawEntries) > 0 {
		prefix := ""
		if i := strings.IndexByte(rawEntries[0].path, '/'); i >= 0 {
			prefix = rawEntries[0].path[:i]
		}
		if prefix != "" {
			allMatch := true
			for _, e := range rawEntries[1:] {
				if !strings.HasPrefix(e.path, prefix+"/") {
					allMatch = false
					break
				}
			}
			if allMatch {
				for i := range rawEntries {
					rawEntries[i].path = strings.TrimPrefix(rawEntries[i].path, prefix+"/")
				}
			}
		}
	}

	// Validate paths and split into SKILL.md vs companions.
	var companions []zipEntry
	var skillMDContent []byte
	for _, e := range rawEntries {
		if !isValidSkillFilePath(e.path) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid file path in zip: %q", e.path)})
			return
		}
		if e.path == "SKILL.md" {
			skillMDContent = e.content
		} else {
			companions = append(companions, e)
		}
	}

	if skillMDContent == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "zip must contain SKILL.md at root"})
		return
	}
	if 1+len(companions) > maxSkillFiles {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf("skill cannot exceed %d files", maxSkillFiles)})
		return
	}

	ofsPath := fmt.Sprintf("%s/resources/skills/%s/", u.UserName, name)

	if err := h.ofsWriter.PutObject(c.Request.Context(), ofsPath+"SKILL.md", skillMDContent); err != nil {
		logger.Default().Error("put SKILL.md to OFS", "key", ofsPath+"SKILL.md", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store resource"})
		return
	}

	initMeta, _ := json.Marshal(db.SkillMeta{Files: []string{"SKILL.md"}})
	rec, err := h.kindsRepo.Create(c.Request.Context(), u.ID, "skill", name, ofsPath, initMeta)
	if err != nil {
		logger.Default().Error("create kind record for zip skill", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create resource"})
		return
	}

	if len(companions) > 0 {
		files := []string{"SKILL.md"}
		for _, entry := range companions {
			ofsKey := ofsPath + entry.path
			if err := h.ofsWriter.PutObject(c.Request.Context(), ofsKey, entry.content); err != nil {
				logger.Default().Error("put companion file to OFS", "key", ofsKey, "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to store file %q", entry.path)})
				return
			}
			files = append(files, entry.path)
		}
		newMeta, _ := json.Marshal(db.SkillMeta{Files: files})
		rec, err = h.kindsRepo.Update(c.Request.Context(), rec.ID, u.ID, db.KindUpdate{Meta: newMeta})
		if err != nil {
			logger.Default().Error("update skill meta after zip upload", "id", rec.ID, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update resource"})
			return
		}
	}

	c.JSON(http.StatusCreated, kindRecordToResponse(rec))
}

// ListResources handles GET /api/resources.
//
// @Summary      List resources
// @Description  List all resources owned by the authenticated user.
// @Tags         resources
// @Produce      json
// @Success      200  {array}   resourceResponse
// @Failure      401  {object}  errorResponse  "unauthorized"
// @Failure      500  {object}  errorResponse  "failed to list resources"
// @Failure      503  {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources [get]
func (h *Handler) ListResources(c *gin.Context) {
	if h.kindsRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	records, err := h.kindsRepo.List(c.Request.Context(), u.ID)
	if err != nil {
		logger.Default().Error("list resources", "user_id", u.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list resources"})
		return
	}

	items := make([]resourceResponse, len(records))
	for i, r := range records {
		items[i] = kindRecordToResponse(r)
	}
	c.JSON(http.StatusOK, items)
}

// UpdateResource handles PUT /api/resources/:id.
//
// @Summary      Update a resource
// @Description  Update a resource's content, meta, or active flag.
// @Tags         resources
// @Accept       json
// @Produce      json
// @Param        id    path      int                    true  "Resource ID"
// @Param        body  body      updateResourceRequest  true  "Update fields"
// @Success      200   {object}  resourceResponse
// @Failure      400   {object}  errorResponse  "invalid request body"
// @Failure      401   {object}  errorResponse  "unauthorized"
// @Failure      404   {object}  errorResponse  "resource not found"
// @Failure      500   {object}  errorResponse  "failed to update resource"
// @Failure      503   {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id} [put]
func (h *Handler) UpdateResource(c *gin.Context) {
	if h.kindsRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var body updateResourceRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	update := db.KindUpdate{IsActive: body.IsActive}

	if body.Content != "" {
		if h.ofsWriter == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
			return
		}
		rec, err := h.kindsRepo.Get(c.Request.Context(), id, u.ID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
			return
		}

		var ofsKey string
		var ofsContent []byte
		switch rec.Kind {
		case "skill":
			ofsKey = rec.OFSPath + "SKILL.md"
			ofsContent = []byte(body.Content)
		case "mcp":
			if !json.Valid([]byte(body.Content)) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "content must be valid JSON for mcp kind"})
				return
			}
			ofsKey = rec.OFSPath
			ofsContent = []byte(body.Content)
			update.Meta = json.RawMessage(body.Content)
		}

		if err := h.ofsWriter.PutObject(c.Request.Context(), ofsKey, ofsContent); err != nil {
			logger.Default().Error("update resource in OFS", "key", ofsKey, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update resource"})
			return
		}
	}

	if len(body.Meta) > 0 && update.Meta == nil {
		update.Meta = body.Meta
	}

	result, err := h.kindsRepo.Update(c.Request.Context(), id, u.ID, update)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}

	c.JSON(http.StatusOK, kindRecordToResponse(result))
}

// DeleteResource handles DELETE /api/resources/:id.
//
// @Summary      Delete a resource
// @Description  Delete a resource owned by the authenticated user. OFS content is not removed.
// @Tags         resources
// @Param        id   path  int  true  "Resource ID"
// @Success      204
// @Failure      400  {object}  errorResponse  "invalid id"
// @Failure      401  {object}  errorResponse  "unauthorized"
// @Failure      404  {object}  errorResponse  "resource not found"
// @Failure      503  {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id} [delete]
func (h *Handler) DeleteResource(c *gin.Context) {
	if h.kindsRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.kindsRepo.Delete(c.Request.Context(), id, u.ID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}

	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}

// GetSkillContent handles GET /api/resources/:id/content.
// Returns the SKILL.md content for a skill resource as plain text.
//
// @Summary      Get skill SKILL.md content
// @Description  Reads and returns the SKILL.md content for a skill resource from OFS.
// @Tags         resources
// @Produce      text/plain
// @Param        id   path      int  true  "Resource ID"
// @Success      200  {string}  string  "SKILL.md content"
// @Failure      400  {object}  errorResponse  "resource is not a skill"
// @Failure      401  {object}  errorResponse  "unauthorized"
// @Failure      404  {object}  errorResponse  "resource not found"
// @Failure      503  {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id}/content [get]
func (h *Handler) GetSkillContent(c *gin.Context) {
	if h.kindsRepo == nil || h.ofsReader == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	rec, err := h.kindsRepo.Get(c.Request.Context(), id, u.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	if rec.Kind != "skill" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content endpoint is only available for skill resources"})
		return
	}

	ofsKey := rec.OFSPath + "SKILL.md"
	data, err := h.ofsReader.GetObjectBytes(c.Request.Context(), ofsKey)
	if err != nil {
		logger.Default().Error("read SKILL.md from OFS", "key", ofsKey, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read skill content"})
		return
	}

	c.Data(http.StatusOK, "text/plain; charset=utf-8", data)
}

// UpsertSkillFile handles PUT /api/resources/:id/files/*filepath.
// Uploads or overwrites a single file inside a skill resource.
//
// @Summary      Upload or overwrite a skill file
// @Description  Upload (or overwrite) a single file inside a skill resource. Body is the raw file content (max 1 MiB). Skills are capped at 20 files.
// @Tags         resources
// @Accept       octet-stream
// @Produce      json
// @Param        id        path      int     true  "Resource ID"
// @Param        filepath  path      string  true  "Relative file path inside the skill"
// @Param        body      body      string  true  "Raw file bytes"
// @Success      200       {object}  resourceResponse
// @Failure      400       {object}  errorResponse  "invalid id or file path"
// @Failure      401       {object}  errorResponse  "unauthorized"
// @Failure      404       {object}  errorResponse  "resource not found"
// @Failure      413       {object}  errorResponse  "file exceeds size limit"
// @Failure      422       {object}  errorResponse  "skill file count exceeds limit"
// @Failure      500       {object}  errorResponse  "failed to store file"
// @Failure      503       {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id}/files/{filepath} [put]
func (h *Handler) UpsertSkillFile(c *gin.Context) {
	if h.kindsRepo == nil || h.ofsWriter == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	// Gin captures *filepath with a leading "/"; strip it for a clean relative path.
	filePath := strings.TrimPrefix(c.Param("filepath"), "/")
	if !isValidSkillFilePath(filePath) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file path: must match [a-zA-Z0-9_./-]+ with no empty, '.', or '..' segments"})
		return
	}

	content, err := io.ReadAll(io.LimitReader(c.Request.Body, maxSkillFileSize+1))
	if err != nil {
		logger.Default().Error("read skill file body", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read request body"})
		return
	}
	if len(content) > maxSkillFileSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file exceeds %d MiB limit", maxSkillFileSize>>20)})
		return
	}

	rec, err := h.kindsRepo.Get(c.Request.Context(), id, u.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	if rec.Kind != "skill" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file management is only supported for skill resources"})
		return
	}

	files := rec.SkillFiles()
	isNew := true
	for _, f := range files {
		if f == filePath {
			isNew = false
			break
		}
	}
	if isNew && len(files) >= maxSkillFiles {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf("skill cannot exceed %d files", maxSkillFiles)})
		return
	}

	ofsKey := rec.OFSPath + filePath
	if err := h.ofsWriter.PutObject(c.Request.Context(), ofsKey, content); err != nil {
		logger.Default().Error("put skill file to OFS", "key", ofsKey, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store file"})
		return
	}

	var result *db.KindRecord
	if isNew {
		newMeta, _ := json.Marshal(db.SkillMeta{Files: append(files, filePath)})
		result, err = h.kindsRepo.Update(c.Request.Context(), id, u.ID, db.KindUpdate{Meta: newMeta})
		if err != nil {
			logger.Default().Error("update skill meta", "id", id, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update resource"})
			return
		}
	} else {
		result = rec
	}

	c.JSON(http.StatusOK, kindRecordToResponse(result))
}

// DeleteSkillFile handles DELETE /api/resources/:id/files/*filepath.
// Removes a file from the skill's manifest. SKILL.md cannot be removed.
// OFS content is not deleted (consistent with resource-level delete behavior).
//
// @Summary      Remove a file from a skill
// @Description  Remove a file from a skill's manifest. SKILL.md cannot be removed; OFS content is not deleted.
// @Tags         resources
// @Produce      json
// @Param        id        path      int     true  "Resource ID"
// @Param        filepath  path      string  true  "Relative file path inside the skill"
// @Success      200       {object}  resourceResponse
// @Failure      400       {object}  errorResponse  "invalid id, invalid path, or SKILL.md cannot be removed"
// @Failure      401       {object}  errorResponse  "unauthorized"
// @Failure      404       {object}  errorResponse  "resource or file not found"
// @Failure      500       {object}  errorResponse  "failed to update resource"
// @Failure      503       {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id}/files/{filepath} [delete]
func (h *Handler) DeleteSkillFile(c *gin.Context) {
	if h.kindsRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	filePath := strings.TrimPrefix(c.Param("filepath"), "/")
	if filePath == "SKILL.md" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SKILL.md cannot be removed"})
		return
	}
	if !isValidSkillFilePath(filePath) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file path"})
		return
	}

	rec, err := h.kindsRepo.Get(c.Request.Context(), id, u.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	if rec.Kind != "skill" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file management is only supported for skill resources"})
		return
	}

	files := rec.SkillFiles()
	newFiles := make([]string, 0, len(files))
	found := false
	for _, f := range files {
		if f == filePath {
			found = true
			continue
		}
		newFiles = append(newFiles, f)
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found in skill"})
		return
	}

	newMeta, _ := json.Marshal(db.SkillMeta{Files: newFiles})
	result, err := h.kindsRepo.Update(c.Request.Context(), id, u.ID, db.KindUpdate{Meta: newMeta})
	if err != nil {
		logger.Default().Error("remove skill file from meta", "id", id, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update resource"})
		return
	}

	c.JSON(http.StatusOK, kindRecordToResponse(result))
}

func kindRecordToResponse(r *db.KindRecord) resourceResponse {
	return resourceResponse{
		ID:        r.ID,
		Kind:      r.Kind,
		Name:      r.Name,
		OFSPath:   r.OFSPath,
		Meta:      r.Meta,
		IsActive:  r.IsActive,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

// WorkspaceFiles handles GET /api/tasks/:id/workspace/files?path=<absolute-dir>.
//
// @Summary      List workspace directory via OFS
// @Tags         tasks
// @Param        id    path   string  true  "Task ID"
// @Param        path  query  string  true  "Absolute directory path"
// @Success      200  {array}   FileInfo
// @Failure      400  {object}  errorResponse  "path is required"
// @Failure      404  {object}  errorResponse  "task not found"
// @Failure      409  {object}  errorResponse  "OFS not configured"
// @Failure      500  {object}  errorResponse  "internal error"
// @Router       /api/tasks/{id}/workspace/files [get]
func (h *Handler) WorkspaceFiles(c *gin.Context) {
	taskID := c.Param("id")
	dir := c.Query("path")
	if dir == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "path is required"})
		return
	}

	t, err := h.store.Get(c.Request.Context(), taskID)
	if err != nil || t == nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "task not found"})
		return
	}
	if checkTaskOwner(c, t.Username) {
		return
	}

	if h.workspaceReader == nil {
		c.JSON(http.StatusConflict, errorResponse{Error: "OFS not configured"})
		return
	}

	entries, err := h.workspaceReader.ListWorkspace(c.Request.Context(), t.Username, taskID, dir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	result := make([]FileInfo, len(entries))
	for i, e := range entries {
		result[i] = FileInfo{
			Path:    e.Path,
			Name:    e.Name,
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: e.ModTime.UTC().Format(time.RFC3339),
		}
	}
	c.JSON(http.StatusOK, result)
}

// WorkspaceFile handles GET /api/tasks/:id/workspace/file?path=<absolute-file>.
//
// @Summary      Download workspace file via OFS
// @Tags         tasks
// @Param        id    path   string  true  "Task ID"
// @Param        path  query  string  true  "Absolute file path"
// @Success      200
// @Failure      400  {object}  errorResponse  "path is required"
// @Failure      404  {object}  errorResponse  "task not found or file not found"
// @Failure      409  {object}  errorResponse  "OFS not configured"
// @Router       /api/tasks/{id}/workspace/file [get]
func (h *Handler) WorkspaceFile(c *gin.Context) {
	taskID := c.Param("id")
	filePath := c.Query("path")
	if filePath == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "path is required"})
		return
	}

	t, err := h.store.Get(c.Request.Context(), taskID)
	if err != nil || t == nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "task not found"})
		return
	}
	if checkTaskOwner(c, t.Username) {
		return
	}

	if h.workspaceReader == nil {
		c.JSON(http.StatusConflict, errorResponse{Error: "OFS not configured"})
		return
	}

	data, err := h.workspaceReader.GetWorkspaceFile(c.Request.Context(), t.Username, taskID, filePath)
	if err != nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "file not found"})
		return
	}

	c.Data(http.StatusOK, "application/octet-stream", data)
}

// ExecdProxy proxies filesystem and command requests to the execd daemon running
// inside a task's sandbox on port 44772.
//
// @Summary      Proxy execd filesystem API
// @Description  Forwards any method+path to the execd daemon (port 44772) inside the task's sandbox. Supports GET /files/search, GET /files/download, POST /directories, etc.
// @Tags         tasks
// @Param        id    path  string  true  "Task ID"
// @Param        path  path  string  true  "Execd sub-path (e.g. files/search)"
// @Success      200
// @Failure      404   {object}  errorResponse  "task not found"
// @Failure      409   {object}  errorResponse  "sandbox not running"
// @Failure      502   {object}  errorResponse  "execd unreachable"
// @Router       /api/tasks/{id}/execd/{path} [get]
func (h *Handler) ExecdProxy(c *gin.Context) {
	taskID := c.Param("id")
	subpath := c.Param("path") // begins with "/"

	t, err := h.store.Get(c.Request.Context(), taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "task not found"})
		return
	}

	sandboxID := t.GetSandboxID()
	if sandboxID == "" {
		c.JSON(http.StatusConflict, errorResponse{Error: "sandbox not running"})
		return
	}

	target := fmt.Sprintf("%s/sandboxes/%s/proxy/44772%s", h.serverURL, sandboxID, subpath)
	if q := c.Request.URL.RawQuery; q != "" {
		target += "?" + q
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target, c.Request.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	req.Header.Set("X-OPEN-SANDBOX-API-KEY", h.sandboxAPIKey)
	if ct := c.GetHeader("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorResponse{Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			c.Header(k, v)
		}
	}
	c.Status(resp.StatusCode)
	io.Copy(c.Writer, resp.Body) //nolint:errcheck
}
