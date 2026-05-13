package api

import (
	"encoding/json"
	"time"
)

type createTaskRequest struct {
	Username string            `json:"username" binding:"required"`
	Env      map[string]string `json:"env,omitempty"`
}

type createTaskResponse struct {
	ID string `json:"id"`
}

type sendMessageRequest struct {
	Prompt string `json:"prompt" binding:"required"`
}

type getTaskResponse struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	State     string `json:"state"`
	SandboxID string `json:"sandbox_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	CWD       string `json:"cwd"`
}

// FileInfo is a single file or directory entry in a workspace listing.
// Field names and JSON keys match the execd files/search response shape
// so the frontend requires no parsing changes.
type FileInfo struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
}

type respondToPermissionRequest struct {
	Decision string `json:"decision" binding:"required"` // "allow" or "deny"
}

type respondToQuestionRequest struct {
	Answers map[string]any `json:"answers" binding:"required"`
}

type healthResponse struct {
	Status string `json:"status"`
}

type taskListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type createResourceRequest struct {
	Kind    string          `json:"kind" binding:"required"`
	Name    string          `json:"name" binding:"required"`
	Content string          `json:"content"`
	Meta    json.RawMessage `json:"meta,omitempty"`
}

type updateResourceRequest struct {
	Content  string          `json:"content,omitempty"`
	Meta     json.RawMessage `json:"meta,omitempty"`
	IsActive *bool           `json:"is_active,omitempty"`
}

type resourceResponse struct {
	ID        int             `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	OFSPath   string          `json:"ofs_path"`
	Meta      json.RawMessage `json:"meta"`
	IsActive  bool            `json:"is_active"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type passwordLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type registerRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Email    string `json:"email,omitempty"`
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

