package api

type createTaskRequest struct {
	Username string            `json:"username,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

type createTaskResponse struct {
	ID string `json:"id"`
}

type sendMessageRequest struct {
	Prompt string `json:"prompt"`
}

type getTaskResponse struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	SandboxState   string `json:"sandbox_state"`
	SandboxID      string `json:"sandbox_id"`
	AgentSessionID string `json:"agent_session_id"`
}

type healthResponse struct {
	Status string `json:"status"`
}
