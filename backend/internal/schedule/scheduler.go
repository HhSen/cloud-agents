package schedule

import (
	"context"
	"errors"
	"sync"

	"github.com/robfig/cron/v3"
	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/pkg/logger"
	"gorm.io/gorm"
)

// Scheduler owns the robfig/cron runner and maps schedule IDs to cron entry IDs.
// It is started once at server boot and reloads individual schedules on CRUD operations.
type Scheduler struct {
	c       *cron.Cron
	gormDB  *gorm.DB
	taskSvc TaskService

	mu      sync.Mutex
	entries map[string]cron.EntryID // scheduleID → cron entry
}

func NewScheduler(gormDB *gorm.DB, taskSvc TaskService) *Scheduler {
	return &Scheduler{
		c:       cron.New(),
		gormDB:  gormDB,
		taskSvc: taskSvc,
		entries: make(map[string]cron.EntryID),
	}
}

// Start loads all enabled schedules and begins the cron runner.
func (s *Scheduler) Start(ctx context.Context) {
	var recs []db.ScheduledTask
	if err := s.gormDB.WithContext(ctx).Where("enabled = ?", true).Find(&recs).Error; err != nil {
		logger.Default().Error("schedule: load initial schedules", "err", err)
	}
	for _, rec := range recs {
		s.register(rec)
	}
	s.c.Start()
	logger.Default().Info("scheduler started", "schedules", len(recs))
}

// Stop gracefully shuts the cron runner.
func (s *Scheduler) Stop() {
	s.c.Stop()
}

// Reload re-reads a single schedule from the DB and re-registers it.
// Called by Service after Create/Update/Toggle(enable).
func (s *Scheduler) Reload(id string) {
	var rec db.ScheduledTask
	if err := s.gormDB.Where("id = ? AND enabled = ?", id, true).First(&rec).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Default().Error("schedule: reload", "id", id, "err", err)
		}
		return
	}
	s.Remove(id)
	s.register(rec)
}

// Remove unregisters the cron entry for a schedule.
func (s *Scheduler) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if eid, ok := s.entries[id]; ok {
		s.c.Remove(eid)
		delete(s.entries, id)
	}
}

func (s *Scheduler) register(rec db.ScheduledTask) {
	expr := rec.CronExpr
	if expr == "@once" {
		// One-shot: use @once wrapper that fires at RunAt and removes itself.
		s.registerOnce(rec)
		return
	}
	eid, err := s.c.AddFunc(expr, func() { s.fire(rec.ID) })
	if err != nil {
		logger.Default().Error("schedule: register cron", "id", rec.ID, "expr", expr, "err", err)
		return
	}
	s.mu.Lock()
	s.entries[rec.ID] = eid
	s.mu.Unlock()
	logger.Default().Info("schedule: registered", "id", rec.ID, "expr", expr)
}

func (s *Scheduler) registerOnce(rec db.ScheduledTask) {
	if rec.RunAt == nil {
		return
	}
	// Use a custom cron.Schedule implementation that fires once at RunAt.
	job := &onceJob{s: s, schedID: rec.ID}
	eid := s.c.Schedule(onceSpec{t: *rec.RunAt}, job)
	s.mu.Lock()
	s.entries[rec.ID] = eid
	s.mu.Unlock()
}

// fire is called by the cron runner for each scheduled firing.
func (s *Scheduler) fire(schedID string) {
	if err := runFire(context.Background(), s.gormDB, s.taskSvc, schedID); err != nil {
		logger.Default().Error("schedule: fire error", "id", schedID, "err", err)
	}
}
