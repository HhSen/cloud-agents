package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/l-lab/cloud-agents/internal/sandbox"
	"github.com/l-lab/cloud-agents/internal/storage"
	"github.com/l-lab/cloud-agents/internal/task"
)

// TaskStore is the storage interface for Task records.
type TaskStore = task.Repository

// SandboxManager provisions and tears down the compute sandbox that backs a task.
type SandboxManager interface {
	ProvisionForTask(ctx context.Context, t *task.Task) error
	DeleteSandbox(ctx context.Context, sandboxID string) error
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
	StreamMessage(ctx context.Context, t *task.Task, prompt string, blocks []sandbox.ContentBlock, w http.ResponseWriter) error
	SteerMessage(ctx context.Context, t *task.Task, prompt, priority string) error
	RespondToPermission(ctx context.Context, t *task.Task, decision string) error
	RespondToQuestion(ctx context.Context, t *task.Task, answers map[string]any) error
}
