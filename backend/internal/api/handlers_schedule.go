package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/l-lab/cloud-agents/internal/auth"
	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/internal/schedule"
	"github.com/l-lab/cloud-agents/pkg/logger"
)

// ScheduleStore is the interface for schedule CRUD used by handlers.
type ScheduleStore interface {
	Create(ctx context.Context, userID uint, req schedule.CreateRequest) (*db.ScheduledTask, error)
	Update(ctx context.Context, id string, userID uint, req schedule.UpdateRequest) (*db.ScheduledTask, error)
	Delete(ctx context.Context, id string, userID uint) error
	Get(ctx context.Context, id string, userID uint) (*db.ScheduledTask, error)
	List(ctx context.Context, userID uint) ([]db.ScheduledTask, error)
	Toggle(ctx context.Context, id string, userID uint, enabled bool) error
}

// ScheduleHandler serves the schedule CRUD API.
type ScheduleHandler struct {
	store     ScheduleStore
	taskStore TaskStore
	manager   SandboxManager
	proxy     MessageProxy
}

// NewScheduleHandler constructs a ScheduleHandler from its dependencies.
func NewScheduleHandler(store ScheduleStore, taskStore TaskStore, mgr SandboxManager, proxy MessageProxy) *ScheduleHandler {
	return &ScheduleHandler{
		store:     store,
		taskStore: taskStore,
		manager:   mgr,
		proxy:     proxy,
	}
}

func scheduleRecordToResponse(rec *db.ScheduledTask) scheduleResponse {
	var extraEnv map[string]string
	if rec.ExtraEnv != "" && rec.ExtraEnv != "null" {
		_ = json.Unmarshal([]byte(rec.ExtraEnv), &extraEnv)
	}
	return scheduleResponse{
		ID:          rec.ID,
		Title:       rec.Title,
		Prompt:      rec.Prompt,
		CronExpr:    rec.CronExpr,
		RunAt:       rec.RunAt,
		ExtraEnv:    extraEnv,
		GitURL:      rec.GitURL,
		TimeoutSecs: rec.TimeoutSecs,
		Concurrency: rec.Concurrency,
		Enabled:     rec.Enabled,
		LastRunAt:   rec.LastRunAt,
		NextRunAt:   rec.NextRunAt,
		CreatedAt:   rec.CreatedAt,
	}
}

// ListSchedules handles GET /api/schedules.
//
// @Summary      List schedules
// @Description  List all schedules for the authenticated user.
// @Tags         schedules
// @Produce      json
// @Success      200  {array}   scheduleResponse
// @Failure      401  {object}  errorResponse  "unauthorized"
// @Router       /api/schedules [get]
func (h *ScheduleHandler) ListSchedules(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	recs, err := h.store.List(c.Request.Context(), u.ID)
	if err != nil {
		logger.Default().Error("list schedules", "err", err)
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}
	items := make([]scheduleResponse, len(recs))
	for i := range recs {
		items[i] = scheduleRecordToResponse(&recs[i])
	}
	c.JSON(http.StatusOK, items)
}

// CreateSchedule handles POST /api/schedules.
//
// @Summary      Create a schedule
// @Description  Create a new scheduled task.
// @Tags         schedules
// @Accept       json
// @Produce      json
// @Param        body  body      createScheduleRequest  true  "Create schedule request"
// @Success      201   {object}  scheduleResponse
// @Failure      400   {object}  errorResponse  "invalid request"
// @Failure      401   {object}  errorResponse  "unauthorized"
// @Router       /api/schedules [post]
func (h *ScheduleHandler) CreateSchedule(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	var body createScheduleRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	rec, err := h.store.Create(c.Request.Context(), u.ID, schedule.CreateRequest{
		Title:       body.Title,
		Prompt:      body.Prompt,
		CronExpr:    body.CronExpr,
		RunAt:       body.RunAt,
		ExtraEnv:    body.ExtraEnv,
		GitURL:      body.GitURL,
		TimeoutSecs: body.TimeoutSecs,
		Concurrency: body.Concurrency,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusCreated, scheduleRecordToResponse(rec))
}

// GetSchedule handles GET /api/schedules/:id.
//
// @Summary      Get a schedule
// @Tags         schedules
// @Produce      json
// @Param        id   path      string  true  "Schedule ID"
// @Success      200  {object}  scheduleResponse
// @Failure      404  {object}  errorResponse
// @Router       /api/schedules/{id} [get]
func (h *ScheduleHandler) GetSchedule(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	rec, err := h.store.Get(c.Request.Context(), c.Param("id"), u.ID)
	if err != nil {
		if err == schedule.ErrNotFound {
			c.JSON(http.StatusNotFound, errorResponse{Error: "schedule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}
	c.JSON(http.StatusOK, scheduleRecordToResponse(rec))
}

// UpdateSchedule handles PUT /api/schedules/:id.
//
// @Summary      Update a schedule
// @Tags         schedules
// @Accept       json
// @Produce      json
// @Param        id    path      string                 true  "Schedule ID"
// @Param        body  body      updateScheduleRequest  true  "Update"
// @Success      200   {object}  scheduleResponse
// @Failure      400   {object}  errorResponse
// @Failure      404   {object}  errorResponse
// @Router       /api/schedules/{id} [put]
func (h *ScheduleHandler) UpdateSchedule(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	var body updateScheduleRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	rec, err := h.store.Update(c.Request.Context(), c.Param("id"), u.ID, schedule.UpdateRequest{
		Title:       body.Title,
		Prompt:      body.Prompt,
		CronExpr:    body.CronExpr,
		RunAt:       body.RunAt,
		ExtraEnv:    body.ExtraEnv,
		GitURL:      body.GitURL,
		TimeoutSecs: body.TimeoutSecs,
		Concurrency: body.Concurrency,
	})
	if err != nil {
		if err == schedule.ErrNotFound {
			c.JSON(http.StatusNotFound, errorResponse{Error: "schedule not found"})
			return
		}
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, scheduleRecordToResponse(rec))
}

// DeleteSchedule handles DELETE /api/schedules/:id.
//
// @Summary      Delete a schedule
// @Tags         schedules
// @Param        id  path  string  true  "Schedule ID"
// @Success      204
// @Failure      404  {object}  errorResponse
// @Router       /api/schedules/{id} [delete]
func (h *ScheduleHandler) DeleteSchedule(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	if err := h.store.Delete(c.Request.Context(), c.Param("id"), u.ID); err != nil {
		if err == schedule.ErrNotFound {
			c.JSON(http.StatusNotFound, errorResponse{Error: "schedule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}
	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}

// EnableSchedule handles POST /api/schedules/:id/enable.
//
// @Summary      Enable a schedule
// @Tags         schedules
// @Param        id  path  string  true  "Schedule ID"
// @Success      204
// @Router       /api/schedules/{id}/enable [post]
func (h *ScheduleHandler) EnableSchedule(c *gin.Context) {
	h.toggleSchedule(c, true)
}

// DisableSchedule handles POST /api/schedules/:id/disable.
//
// @Summary      Disable a schedule
// @Tags         schedules
// @Param        id  path  string  true  "Schedule ID"
// @Success      204
// @Router       /api/schedules/{id}/disable [post]
func (h *ScheduleHandler) DisableSchedule(c *gin.Context) {
	h.toggleSchedule(c, false)
}

func (h *ScheduleHandler) toggleSchedule(c *gin.Context, enabled bool) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	if err := h.store.Toggle(c.Request.Context(), c.Param("id"), u.ID, enabled); err != nil {
		if err == schedule.ErrNotFound {
			c.JSON(http.StatusNotFound, errorResponse{Error: "schedule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}
	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}

// RunScheduleNow handles POST /api/schedules/:id/run.
//
// @Summary      Trigger a manual run of a schedule
// @Description  Fires the schedule immediately and returns the task ID of the spawned run.
// @Tags         schedules
// @Param        id  path  string  true  "Schedule ID"
// @Success      200  {object}  scheduleRunResponse
// @Failure      404  {object}  errorResponse
// @Router       /api/schedules/{id}/run [post]
func (h *ScheduleHandler) RunScheduleNow(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	rec, err := h.store.Get(c.Request.Context(), c.Param("id"), u.ID)
	if err != nil {
		if err == schedule.ErrNotFound {
			c.JSON(http.StatusNotFound, errorResponse{Error: "schedule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}

	var extraEnv map[string]string
	if rec.ExtraEnv != "" && rec.ExtraEnv != "null" {
		_ = json.Unmarshal([]byte(rec.ExtraEnv), &extraEnv)
	}

	t, err := h.taskStore.Create(c.Request.Context(), u.UserName, extraEnv, rec.GitURL, rec.ID)
	if err != nil {
		logger.Default().Error("run schedule now: create task", "err", err)
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to create run task"})
		return
	}
	title := rec.Title
	if title == "" {
		title = rec.ID
	}
	t.SetTitle(fmt.Sprintf("%s – %s", title, time.Now().Format("2006-01-02 15:04")))

	mgr := h.manager
	proxy := h.proxy
	prompt := rec.Prompt
	go func() {
		ctx := context.Background()
		t.SetProvisioning()
		if err := t.EnsureProvisioned(func() error {
			return mgr.ProvisionForTask(ctx, t)
		}); err != nil {
			t.SetError(err.Error())
			return
		}
		_ = proxy.StreamMessage(ctx, t, prompt, nil, &discardWriter{})
	}()

	c.JSON(http.StatusOK, scheduleRunResponse{TaskID: t.ID})
}

// ListScheduleRuns handles GET /api/schedules/:id/runs.
//
// @Summary      List past runs for a schedule
// @Tags         schedules
// @Produce      json
// @Param        id  path  string  true  "Schedule ID"
// @Success      200  {array}  runListItem
// @Failure      404  {object}  errorResponse
// @Router       /api/schedules/{id}/runs [get]
func (h *ScheduleHandler) ListScheduleRuns(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	schedID := c.Param("id")
	if _, err := h.store.Get(c.Request.Context(), schedID, u.ID); err != nil {
		if err == schedule.ErrNotFound {
			c.JSON(http.StatusNotFound, errorResponse{Error: "schedule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}
	summaries, err := h.taskStore.ListBySchedule(c.Request.Context(), schedID)
	if err != nil {
		logger.Default().Error("list schedule runs", "err", err)
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}
	items := make([]runListItem, len(summaries))
	for i, s := range summaries {
		items[i] = runListItem{
			ID:        s.ID,
			Title:     s.Title,
			State:     s.State,
			ErrorMsg:  s.ErrorMsg,
			CreatedAt: s.CreatedAt,
			UpdatedAt: s.UpdatedAt,
		}
	}
	c.JSON(http.StatusOK, items)
}

// discardWriter satisfies http.ResponseWriter and http.Flusher, discarding all output.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header {
	if d.h == nil {
		d.h = make(http.Header)
	}
	return d.h
}
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(_ int)           {}
func (d *discardWriter) Flush()                      {}
