package db

import "time"

// ScheduledTask is the template for a recurring or one-shot agent execution.
// Each firing creates a child Task linked back via tasks.schedule_id.
type ScheduledTask struct {
	ID          string     `gorm:"primaryKey;size:36"`
	UserID      uint       `gorm:"not null;index"`
	User        User       `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
	Title       string     `gorm:"size:255"`
	Prompt      string     `gorm:"type:text;not null"`
	CronExpr    string     `gorm:"size:100;not null"`
	RunAt       *time.Time `gorm:"default:null"` // set only for @once schedules
	ExtraEnv    string     `gorm:"type:text"`    // JSON map[string]string
	GitURL      string     `gorm:"size:512"`
	TimeoutSecs int        `gorm:"not null;default:1800"`
	Concurrency int        `gorm:"not null;default:0"` // 0=skip if running, 1=allow parallel
	Enabled     bool       `gorm:"not null;default:true"`
	LastRunAt   *time.Time
	NextRunAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
