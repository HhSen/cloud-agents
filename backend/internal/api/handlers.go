package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
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

// FileStore retrieves task history from OFS-backed file storage.
type FileStore interface {
	ListHistory(ctx context.Context, username, taskID string) ([]string, error)
	GetHistory(ctx context.Context, key string) ([]json.RawMessage, error)
	GetSessionMeta(ctx context.Context, username, taskID string) (*storage.SessionMeta, error)
}

// ResourceWriter writes files to OFS storage.
type ResourceWriter interface {
	PutObject(ctx context.Context, key string, data []byte) error
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
	store     TaskStore
	manager   SandboxManager
	proxy     MessageProxy
	fileStore FileStore
	kindsRepo db.KindsRepository // optional; nil disables resource API
	ofsWriter ResourceWriter     // optional; nil disables resource content upload
}

// NewHandler constructs a Handler from its dependencies.
func NewHandler(store TaskStore, mgr SandboxManager, proxy MessageProxy, fileStore FileStore) *Handler {
	return &Handler{
		store:     store,
		manager:   mgr,
		proxy:     proxy,
		fileStore: fileStore,
	}
}

func (h *Handler) withResources(kr db.KindsRepository, w ResourceWriter) {
	h.kindsRepo = kr
	h.ofsWriter = w
}

// PasswordLogin returns a handler for POST /api/auth/login (username + password).
// PasswordLoginHandler returns a Gin handler for POST /api/auth/login.
func PasswordLoginHandler(gormDB *gorm.DB, authCfg config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Username string `json:"username" binding:"required"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "username and password required"})
			return
		}
		user, err := db.FindByCredentials(gormDB, body.Username, body.Password)
		if err != nil {
			logger.Default().Error("password login db error", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		if user == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
		ttl := time.Duration(authCfg.TokenTTLSeconds) * time.Second
		token, err := auth.CreateToken(authCfg.SecretKey, ttl, user.ID, user.UserName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue token"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"access_token": token})
	}
}

// RegisterHandler handles POST /api/auth/register (username + password + email).
func RegisterHandler(gormDB *gorm.DB, authCfg config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Username string `json:"username" binding:"required"`
			Password string `json:"password" binding:"required"`
			Email    string `json:"email"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "username and password required"})
			return
		}
		if body.Email == "" {
			body.Email = body.Username + "@local"
		}
		user, err := db.CreateWithPassword(gormDB, body.Username, body.Email, body.Password)
		if err != nil {
			logger.Default().Error("register user", "err", err)
			c.JSON(http.StatusConflict, gin.H{"error": "username already taken"})
			return
		}
		ttl := time.Duration(authCfg.TokenTTLSeconds) * time.Second
		token, err := auth.CreateToken(authCfg.SecretKey, ttl, user.ID, user.UserName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue token"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"access_token": token})
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
		ofsPath = fmt.Sprintf("%s/resources/skills/%s/", u.UserName, body.Name)
		ofsKey = ofsPath + "SKILL.md"
		ofsContent = []byte(body.Content)
		meta = body.Meta
		if len(meta) == 0 {
			meta = json.RawMessage("{}")
		}
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

// ListResources handles GET /api/resources.
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
