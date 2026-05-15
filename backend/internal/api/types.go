package api

import "time"

// Shared request/response types used across multiple handler domains.

type createTaskRequest struct {
	Username string            `json:"username" binding:"required"`
	Title    string            `json:"title,omitempty"`
	GitURL   string            `json:"git_url,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

type createTaskResponse struct {
	ID string `json:"id"`
}

type sendMessageRequest struct {
	Prompt string `json:"prompt" binding:"required"`
}

type steerMessageRequest struct {
	Prompt   string `json:"prompt" binding:"required"`
	Priority string `json:"priority"` // "now" | "next" | "later"
}

type getTaskResponse struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	State     string `json:"state"`
	SandboxID string `json:"sandbox_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	CWD       string `json:"cwd"`
	GitURL    string `json:"git_url,omitempty"`
	ErrorMsg  string `json:"error_msg,omitempty"`
}

type respondToPermissionRequest struct {
	Decision string `json:"decision" binding:"required"`
}

type respondToQuestionRequest struct {
	Answers map[string]any `json:"answers" binding:"required"`
}

type healthResponse struct {
	Status string `json:"status"`
}

type taskListItem struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	State      string    `json:"state"`
	GitURL     string    `json:"git_url,omitempty"`
	ErrorMsg   string    `json:"error_msg,omitempty"`
	ScheduleID string    `json:"schedule_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type runtimeConfigResponse struct {
	LoginMode     string `json:"loginMode"`
	PasswordLogin bool   `json:"passwordLogin"`
	AllowRegister bool   `json:"allowRegister"`
	OIDCLoginText string `json:"oidcLoginText,omitempty"`
	SSOLoginText  string `json:"ssoLoginText,omitempty"`
}


// ---- schedule types ----

type createScheduleRequest struct {
	Title       string            `json:"title"`
	Prompt      string            `json:"prompt" binding:"required"`
	CronExpr    string            `json:"cron_expr" binding:"required"`
	RunAt       *time.Time        `json:"run_at"`
	ExtraEnv    map[string]string `json:"extra_env"`
	GitURL      string            `json:"git_url"`
	TimeoutSecs int               `json:"timeout_secs"`
	Concurrency int               `json:"concurrency"`
}

type updateScheduleRequest struct {
	Title       *string           `json:"title"`
	Prompt      *string           `json:"prompt"`
	CronExpr    *string           `json:"cron_expr"`
	RunAt       *time.Time        `json:"run_at"`
	ExtraEnv    map[string]string `json:"extra_env"`
	GitURL      *string           `json:"git_url"`
	TimeoutSecs *int              `json:"timeout_secs"`
	Concurrency *int              `json:"concurrency"`
}

type scheduleResponse struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Prompt      string            `json:"prompt"`
	CronExpr    string            `json:"cron_expr"`
	RunAt       *time.Time        `json:"run_at,omitempty"`
	ExtraEnv    map[string]string `json:"extra_env,omitempty"`
	GitURL      string            `json:"git_url,omitempty"`
	TimeoutSecs int               `json:"timeout_secs"`
	Concurrency int               `json:"concurrency"`
	Enabled     bool              `json:"enabled"`
	LastRunAt   *time.Time        `json:"last_run_at,omitempty"`
	NextRunAt   *time.Time        `json:"next_run_at,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

type scheduleRunResponse struct {
	TaskID string `json:"task_id"`
}

type runListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	ErrorMsg  string    `json:"error_msg,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
