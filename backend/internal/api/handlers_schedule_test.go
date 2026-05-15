package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/l-lab/cloud-agents/internal/auth"
	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/internal/schedule"
	"github.com/l-lab/cloud-agents/internal/task"
)

// ---- mock ScheduleStore ----

type mockScheduleStore struct {
	mu      sync.Mutex
	records map[string]*db.ScheduledTask
	err     error // when non-nil, all ops return this error
}

func newMockScheduleStore() *mockScheduleStore {
	return &mockScheduleStore{records: make(map[string]*db.ScheduledTask)}
}

func (m *mockScheduleStore) Create(_ context.Context, userID uint, req schedule.CreateRequest) (*db.ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	rec := &db.ScheduledTask{
		ID:          "sched-new",
		UserID:      userID,
		Title:       req.Title,
		Prompt:      req.Prompt,
		CronExpr:    req.CronExpr,
		TimeoutSecs: 1800,
		Enabled:     true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	m.records[rec.ID] = rec
	return rec, nil
}

func (m *mockScheduleStore) Update(_ context.Context, id string, userID uint, req schedule.UpdateRequest) (*db.ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	rec, ok := m.records[id]
	if !ok || rec.UserID != userID {
		return nil, schedule.ErrNotFound
	}
	if req.Title != nil {
		rec.Title = *req.Title
	}
	if req.Prompt != nil {
		rec.Prompt = *req.Prompt
	}
	return rec, nil
}

func (m *mockScheduleStore) Delete(_ context.Context, id string, userID uint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	rec, ok := m.records[id]
	if !ok || rec.UserID != userID {
		return schedule.ErrNotFound
	}
	delete(m.records, id)
	return nil
}

func (m *mockScheduleStore) Get(_ context.Context, id string, userID uint) (*db.ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	rec, ok := m.records[id]
	if !ok || rec.UserID != userID {
		return nil, schedule.ErrNotFound
	}
	return rec, nil
}

func (m *mockScheduleStore) List(_ context.Context, userID uint) ([]db.ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []db.ScheduledTask
	for _, rec := range m.records {
		if rec.UserID == userID {
			out = append(out, *rec)
		}
	}
	return out, nil
}

func (m *mockScheduleStore) Toggle(_ context.Context, id string, userID uint, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	rec, ok := m.records[id]
	if !ok || rec.UserID != userID {
		return schedule.ErrNotFound
	}
	rec.Enabled = enabled
	return nil
}

// seed adds a schedule record directly (for test setup).
func (m *mockScheduleStore) seed(rec *db.ScheduledTask) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[rec.ID] = rec
}

// ---- test context helpers ----

const testUserID = uint(42)

// scheduleCtx injects a test user into a gin.Context.
func scheduleCtx(req *http.Request) (*gin.Context, *httptest.ResponseRecorder) {
	rw := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rw)
	c.Request = req
	auth.SetUser(c, &db.User{ID: testUserID, UserName: "testuser"})
	return c, rw
}

// ---- handler constructor ----

func newScheduleHandler(store *mockScheduleStore, tasks *mockStore) *ScheduleHandler {
	return NewScheduleHandler(store, tasks, &mockManager{}, &mockProxy{})
}

// ---- ListSchedules ----

func TestListSchedules_Empty(t *testing.T) {
	h := newScheduleHandler(newMockScheduleStore(), &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
	h.ListSchedules(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	var items []scheduleResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty list, got %v", items)
	}
}

func TestListSchedules_WithRecords(t *testing.T) {
	store := newMockScheduleStore()
	store.seed(&db.ScheduledTask{
		ID: "s1", UserID: testUserID, Prompt: "p", CronExpr: "@daily", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	h := newScheduleHandler(store, &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
	h.ListSchedules(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	var items []scheduleResponse
	json.Unmarshal(rw.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Errorf("expected 1 item, got %d", len(items))
	}
}

func TestListSchedules_Unauthorized(t *testing.T) {
	h := newScheduleHandler(newMockScheduleStore(), &mockStore{})

	rw := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rw)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/schedules", nil)
	// no user injected
	h.ListSchedules(c)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
}

// ---- CreateSchedule ----

func TestCreateSchedule_Valid(t *testing.T) {
	h := newScheduleHandler(newMockScheduleStore(), &mockStore{})

	body := `{"prompt":"do something","cron_expr":"@daily","timeout_secs":300}`
	c, rw := scheduleCtx(httptest.NewRequest(http.MethodPost, "/api/schedules", strings.NewReader(body)))
	h.CreateSchedule(c)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rw.Code, rw.Body.String())
	}
	var resp scheduleResponse
	json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.ID == "" {
		t.Error("expected non-empty ID in response")
	}
	if resp.CronExpr != "@daily" {
		t.Errorf("CronExpr = %q, want @daily", resp.CronExpr)
	}
}

func TestCreateSchedule_MissingPrompt(t *testing.T) {
	h := newScheduleHandler(newMockScheduleStore(), &mockStore{})

	body := `{"cron_expr":"@daily"}`
	c, rw := scheduleCtx(httptest.NewRequest(http.MethodPost, "/api/schedules", strings.NewReader(body)))
	h.CreateSchedule(c)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
}

func TestCreateSchedule_StoreError(t *testing.T) {
	store := newMockScheduleStore()
	store.err = errors.New("invalid cron_expr: boom")
	h := newScheduleHandler(store, &mockStore{})

	body := `{"prompt":"p","cron_expr":"@daily"}`
	c, rw := scheduleCtx(httptest.NewRequest(http.MethodPost, "/api/schedules", strings.NewReader(body)))
	h.CreateSchedule(c)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
}

// ---- GetSchedule ----

func TestGetSchedule_Found(t *testing.T) {
	store := newMockScheduleStore()
	store.seed(&db.ScheduledTask{
		ID: "s1", UserID: testUserID, Prompt: "p", CronExpr: "@daily", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	h := newScheduleHandler(store, &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodGet, "/api/schedules/s1", nil))
	c.Params = gin.Params{{Key: "id", Value: "s1"}}
	h.GetSchedule(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	var resp scheduleResponse
	json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.ID != "s1" {
		t.Errorf("expected ID=s1, got %q", resp.ID)
	}
}

func TestGetSchedule_NotFound(t *testing.T) {
	h := newScheduleHandler(newMockScheduleStore(), &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodGet, "/api/schedules/nope", nil))
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.GetSchedule(c)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

// ---- UpdateSchedule ----

func TestUpdateSchedule_Ok(t *testing.T) {
	store := newMockScheduleStore()
	store.seed(&db.ScheduledTask{
		ID: "s1", UserID: testUserID, Title: "old", Prompt: "p", CronExpr: "@daily",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	h := newScheduleHandler(store, &mockStore{})

	body := `{"title":"new title"}`
	c, rw := scheduleCtx(httptest.NewRequest(http.MethodPut, "/api/schedules/s1", strings.NewReader(body)))
	c.Params = gin.Params{{Key: "id", Value: "s1"}}
	h.UpdateSchedule(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}
	var resp scheduleResponse
	json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.Title != "new title" {
		t.Errorf("Title = %q, want %q", resp.Title, "new title")
	}
}

func TestUpdateSchedule_NotFound(t *testing.T) {
	h := newScheduleHandler(newMockScheduleStore(), &mockStore{})

	body := `{"title":"x"}`
	c, rw := scheduleCtx(httptest.NewRequest(http.MethodPut, "/api/schedules/nope", strings.NewReader(body)))
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.UpdateSchedule(c)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

// ---- DeleteSchedule ----

func TestDeleteSchedule_Ok(t *testing.T) {
	store := newMockScheduleStore()
	store.seed(&db.ScheduledTask{
		ID: "s1", UserID: testUserID, Prompt: "p", CronExpr: "@daily",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	h := newScheduleHandler(store, &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodDelete, "/api/schedules/s1", nil))
	c.Params = gin.Params{{Key: "id", Value: "s1"}}
	h.DeleteSchedule(c)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rw.Code)
	}
}

func TestDeleteSchedule_NotFound(t *testing.T) {
	h := newScheduleHandler(newMockScheduleStore(), &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodDelete, "/api/schedules/nope", nil))
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.DeleteSchedule(c)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

// ---- Enable / Disable ----

func TestEnableSchedule_Ok(t *testing.T) {
	store := newMockScheduleStore()
	store.seed(&db.ScheduledTask{
		ID: "s1", UserID: testUserID, Prompt: "p", CronExpr: "@daily",
		Enabled: false, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	h := newScheduleHandler(store, &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodPost, "/api/schedules/s1/enable", nil))
	c.Params = gin.Params{{Key: "id", Value: "s1"}}
	h.EnableSchedule(c)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rw.Code)
	}
	store.mu.Lock()
	enabled := store.records["s1"].Enabled
	store.mu.Unlock()
	if !enabled {
		t.Error("expected record to be enabled after EnableSchedule")
	}
}

func TestDisableSchedule_Ok(t *testing.T) {
	store := newMockScheduleStore()
	store.seed(&db.ScheduledTask{
		ID: "s1", UserID: testUserID, Prompt: "p", CronExpr: "@daily",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	h := newScheduleHandler(store, &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodPost, "/api/schedules/s1/disable", nil))
	c.Params = gin.Params{{Key: "id", Value: "s1"}}
	h.DisableSchedule(c)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rw.Code)
	}
	store.mu.Lock()
	enabled := store.records["s1"].Enabled
	store.mu.Unlock()
	if enabled {
		t.Error("expected record to be disabled after DisableSchedule")
	}
}

// ---- RunScheduleNow ----

func TestRunScheduleNow_Ok(t *testing.T) {
	store := newMockScheduleStore()
	store.seed(&db.ScheduledTask{
		ID: "s1", UserID: testUserID, Prompt: "run me", CronExpr: "@daily",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	tasks := &mockStore{}
	h := newScheduleHandler(store, tasks)

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodPost, "/api/schedules/s1/run", nil))
	c.Params = gin.Params{{Key: "id", Value: "s1"}}
	h.RunScheduleNow(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}
	var resp scheduleRunResponse
	json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.TaskID == "" {
		t.Error("expected non-empty task_id in response")
	}
}

func TestRunScheduleNow_NotFound(t *testing.T) {
	h := newScheduleHandler(newMockScheduleStore(), &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodPost, "/api/schedules/nope/run", nil))
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.RunScheduleNow(c)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

// ---- ListScheduleRuns ----

func TestListScheduleRuns_Ok(t *testing.T) {
	store := newMockScheduleStore()
	store.seed(&db.ScheduledTask{
		ID: "s1", UserID: testUserID, Prompt: "p", CronExpr: "@daily",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	tasks := &mockStore{}
	// Inject a pre-created task so ListBySchedule returns something.
	tasks.mu.Lock()
	s := task.NewStore()
	tasks.task = s.Create("testuser", nil, "")
	tasks.mu.Unlock()

	h := newScheduleHandler(store, tasks)

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodGet, "/api/schedules/s1/runs", nil))
	c.Params = gin.Params{{Key: "id", Value: "s1"}}
	h.ListScheduleRuns(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}
	var items []runListItem
	if err := json.Unmarshal(rw.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func TestListScheduleRuns_NotFound(t *testing.T) {
	h := newScheduleHandler(newMockScheduleStore(), &mockStore{})

	c, rw := scheduleCtx(httptest.NewRequest(http.MethodGet, "/api/schedules/nope/runs", nil))
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.ListScheduleRuns(c)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}
