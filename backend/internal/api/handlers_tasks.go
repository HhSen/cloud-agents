package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/l-lab/cloud-agents/internal/auth"
	"github.com/l-lab/cloud-agents/internal/task"
	"github.com/l-lab/cloud-agents/pkg/logger"
)

var validGitURL = regexp.MustCompile("^(https?://|git@|ssh://)[^\\s;|&$`()\\n\\r<>]+$")

func isValidGitURL(u string) bool { return validGitURL.MatchString(u) }

func isPrivateGitURL(u string) bool {
	return strings.HasPrefix(u, "git@") || strings.HasPrefix(u, "ssh://")
}

func repoNameFromGitURL(u string) string {
	u = strings.TrimSuffix(u, ".git")
	if idx := strings.LastIndexAny(u, "/:"); idx >= 0 {
		return u[idx+1:]
	}
	return u
}

// TaskHandler serves the tasks REST API.
type TaskHandler struct {
	store     TaskStore
	manager   SandboxManager
	proxy     MessageProxy
	fileStore FileStore
}

// NewTaskHandler constructs a TaskHandler from its dependencies.
func NewTaskHandler(store TaskStore, mgr SandboxManager, proxy MessageProxy, fileStore FileStore) *TaskHandler {
	return &TaskHandler{
		store:     store,
		manager:   mgr,
		proxy:     proxy,
		fileStore: fileStore,
	}
}

// checkTaskOwner returns true and writes 403 if the authenticated user does not own the task.
// When auth is disabled (no user on context) ownership is not enforced.
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

// Health handles GET /health.
//
// @Summary      Health check
// @Description  Liveness probe — returns ok when the server is up.
// @Tags         health
// @Produce      json
// @Success      200  {object}  healthResponse
// @Router       /health [get]
func (h *TaskHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{Status: "ok"})
}

// CreateTask handles POST /api/tasks.
//
// @Summary      Create a task
// @Description  Create a new task. Optional git_url clones the repository at provision time. Private repos (git@ or ssh://) require an SSH key to be configured.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        body  body      createTaskRequest   true  "Create task request"
// @Success      201   {object}  createTaskResponse
// @Failure      400   {object}  errorResponse  "invalid request body or missing SSH key for private repo"
// @Failure      500   {string}  string  "failed to create task"
// @Router       /api/tasks [post]
func (h *TaskHandler) CreateTask(c *gin.Context) {
	var body createTaskRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.String(http.StatusBadRequest, "invalid request body")
		return
	}

	u := auth.GetUser(c)
	if u != nil {
		body.Username = u.UserName
	}

	if body.GitURL != "" {
		if !isValidGitURL(body.GitURL) {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "git_url must start with https://, git@, or ssh://"})
			return
		}
		if isPrivateGitURL(body.GitURL) && (u == nil || u.SSHPrivateKeyEnc == "") {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "private repo requires an SSH key — add one in Settings"})
			return
		}
		if body.Title == "" {
			body.Title = repoNameFromGitURL(body.GitURL)
		}
	}

	t, err := h.store.Create(c.Request.Context(), body.Username, body.Env, body.GitURL, "")
	if err != nil {
		logger.Default().Error("create task", "err", err)
		c.String(http.StatusInternalServerError, "failed to create task")
		return
	}
	if body.Title != "" {
		t.SetTitle(body.Title)
	}
	c.JSON(http.StatusCreated, createTaskResponse{ID: t.ID})
}

// SendMessage handles POST /api/tasks/:id/messages.
//
// Lazily provisions the task's sandbox on first use, then streams the
// assistant response back to the caller.
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
func (h *TaskHandler) SendMessage(c *gin.Context) {
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

	if err := t.ResetIfExpired(func(sandboxID string) (bool, error) {
		aliveCtx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		alive, err := h.manager.IsSandboxAlive(aliveCtx, sandboxID)
		if err != nil {
			log.Error("sandbox status check failed", "sandbox_id", sandboxID, "err", err)
			return true, err
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

	provisionCtx := context.Background()
	err = t.EnsureProvisioned(func() error {
		return h.manager.ProvisionForTask(provisionCtx, t)
	})
	if err != nil {
		t.SetError(err.Error())
		log.Error("provision failed", "err", err)
		c.String(http.StatusBadGateway, "failed to provision sandbox")
		return
	}

	if err := h.proxy.StreamMessage(c.Request.Context(), t, body.Prompt, c.Writer); err != nil {
		if c.Request.Context().Err() != nil {
			return
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
func (h *TaskHandler) GetTask(c *gin.Context) {
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
		GitURL:    t.GetGitURL(),
		ErrorMsg:  t.GetErrorMsg(),
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
func (h *TaskHandler) DeleteTask(c *gin.Context) {
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
// @Success      200  {array}   object  "Raw session entries"
// @Failure      404  {string}  string  "task not found"
// @Failure      500  {string}  string  "failed to get history"
// @Failure      503  {string}  string  "history storage not configured"
// @Router       /api/tasks/{id}/history [get]
func (h *TaskHandler) GetTaskHistory(c *gin.Context) {
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
func (h *TaskHandler) RespondToPermission(c *gin.Context) {
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
func (h *TaskHandler) RespondToQuestion(c *gin.Context) {
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
func (h *TaskHandler) ListTasks(c *gin.Context) {
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
			GitURL:    t.GitURL,
			ErrorMsg:  t.ErrorMsg,
			CreatedAt: t.CreatedAt,
			UpdatedAt: t.UpdatedAt,
		}
	}
	c.JSON(http.StatusOK, items)
}
