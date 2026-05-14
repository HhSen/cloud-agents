package api

// E2E smoke tests for the messaging flow: register → login → create task →
// stream first message (new session) → stream second message (existing session)
// → respond to permission → respond to question → delete task.
//
// The real router, real sandbox.Proxy, real auth middleware, and a real
// SQLite-backed user table are wired up. The sandbox manager and the
// claude-agent-server upstream are stubbed so the test stays in-process.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/l-lab/cloud-agents/internal/task"
	"github.com/l-lab/cloud-agents/pkg/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/l-lab/cloud-agents/internal/db"
)

// ---- fake upstream (claude-agent-server) ----

// fakeAgentServer simulates the claude-agent-server endpoints used by sandbox.Proxy.
type fakeAgentServer struct {
	mu sync.Mutex

	sessionID string

	// Recorded requests for assertions.
	createCalls    int
	messageCalls   int
	permissionBody string
	questionBody   string
	lastAuth       string
	lastMessageURL string
}

func (f *fakeAgentServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastAuth = r.Header.Get("Authorization")
		f.mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			f.mu.Lock()
			f.createCalls++
			sid := f.sessionID
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "event: session.init\n")
			fmt.Fprintf(w, "data: {\"sessionId\":%q}\n", sid)
			fmt.Fprint(w, "\n")
			fmt.Fprint(w, "event: assistant\n")
			fmt.Fprint(w, "data: {\"text\":\"hello back\"}\n")
			fmt.Fprint(w, "\n")

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/sessions/"):
			// title fetch after a new session
			fmt.Fprint(w, `{"session":{"summary":"Smoke task","customTitle":null,"firstPrompt":""}}`)

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			f.mu.Lock()
			f.messageCalls++
			f.lastMessageURL = r.URL.Path
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "event: assistant\n")
			fmt.Fprint(w, "data: {\"text\":\"second reply\"}\n")
			fmt.Fprint(w, "\n")

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/permissions/respond"):
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.permissionBody = string(b)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/questions/respond"):
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.questionBody = string(b)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		default:
			http.NotFound(w, r)
		}
	})
}

// ---- fake sandbox manager ----

// fakeOFSReader is the read side of fakeOFS, used by fakeManager for skill content.
type fakeOFSReader interface {
	GetObjectBytes(ctx context.Context, key string) ([]byte, error)
}

// fakeManager wires every newly-provisioned task to the upstream test server,
// so the real sandbox.Proxy will dial it.
type fakeManager struct {
	upstreamURL  string
	authHeader   string
	provisionErr error
	provisions   int
	deletes      int

	mu       sync.Mutex
	alive    bool
	aliveErr error

	// Optional: when both are set and t.UserID != 0, ProvisionForTask simulates
	// sandbox.Manager.injectResources — ListActive + GetObjectBytes — and records
	// what content it would have written to which sandbox absolute path.
	kindsRepo db.KindsRepository
	ofsReader fakeOFSReader
	injected  map[string][]byte // sandbox abs path → content (cumulative across provisions)
	injectErr error             // last injection error (non-fatal per spec)
}

func (m *fakeManager) ProvisionForTask(ctx context.Context, t *task.Task) error {
	if m.provisionErr != nil {
		return m.provisionErr
	}
	headers := map[string]string{}
	if m.authHeader != "" {
		headers["Authorization"] = m.authHeader
	}

	// Mirror sandbox.Manager.ProvisionForTask: inject resources after the
	// (simulated) health check passes and before SetRunning.
	if m.kindsRepo != nil && m.ofsReader != nil && t.UserID != 0 {
		if err := m.injectResources(ctx, t); err != nil {
			m.mu.Lock()
			m.injectErr = err
			m.mu.Unlock()
			// non-fatal: provisioning continues
		}
	}

	t.SetRunning("sb-smoke", m.upstreamURL, headers)
	m.mu.Lock()
	m.provisions++
	m.alive = true
	m.mu.Unlock()
	return nil
}

// injectResources mirrors sandbox.Manager.injectResources for tests: it lists
// active resources for t.UserID, reads skill files from OFS, and records the
// content that would be written into the sandbox at the spec'd paths. MCP
// configs are composed into a single .mcp.json under the task CWD.
func (m *fakeManager) injectResources(ctx context.Context, t *task.Task) error {
	kinds, err := m.kindsRepo.ListActive(ctx, t.UserID)
	if err != nil {
		return fmt.Errorf("list active resources: %w", err)
	}
	if len(kinds) == 0 {
		return nil
	}
	taskCWD := fmt.Sprintf("/workspace/%s/%s", t.Username, t.ID)
	skillsDir := taskCWD + "/.claude/skills"

	type mcpConfig struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	mcp := mcpConfig{MCPServers: make(map[string]json.RawMessage)}

	m.mu.Lock()
	if m.injected == nil {
		m.injected = make(map[string][]byte)
	}
	m.mu.Unlock()

	for _, k := range kinds {
		switch k.Kind {
		case "skill":
			for _, relPath := range k.SkillFiles() {
				content, err := m.ofsReader.GetObjectBytes(ctx, k.OFSPath+relPath)
				if err != nil {
					return fmt.Errorf("fetch %q for skill %q: %w", relPath, k.Name, err)
				}
				m.mu.Lock()
				m.injected[skillsDir+"/"+k.Name+"/"+relPath] = content
				m.mu.Unlock()
			}
		case "mcp":
			mcp.MCPServers[k.Name] = k.Meta
		}
	}

	if len(mcp.MCPServers) > 0 {
		data, err := json.Marshal(mcp)
		if err != nil {
			return fmt.Errorf("marshal mcp config: %w", err)
		}
		m.mu.Lock()
		m.injected[taskCWD+"/.mcp.json"] = data
		m.mu.Unlock()
	}
	return nil
}

func (m *fakeManager) DeleteSandbox(_ context.Context, _ string) error {
	m.mu.Lock()
	m.deletes++
	m.alive = false
	m.mu.Unlock()
	return nil
}

func (m *fakeManager) IsSandboxAlive(_ context.Context, _ string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alive, m.aliveErr
}

// ---- fake OFS ----

// fakeOFS is an in-memory OFS implementation that satisfies both the api
// package's ResourceWriter interface (used by the resource API to persist
// content) and the sandbox package's ofsReader interface (used at injection
// time). Holding both roles in one struct lets a smoke test assert that
// content posted via the API is the same content the manager later injects.
type fakeOFS struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeOFS() *fakeOFS {
	return &fakeOFS{objects: make(map[string][]byte)}
}

func (f *fakeOFS) PutObject(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	f.objects[key] = cp
	return nil
}

func (f *fakeOFS) GetObjectBytes(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("ofs: object %q not found", key)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

// ---- harness ----

type smokeHarness struct {
	t        *testing.T
	router   http.Handler
	upstream *httptest.Server
	agent    *fakeAgentServer
	manager  *fakeManager
	token    string
	user     *db.User

	gormDB    *gorm.DB
	store     *task.MemoryRepository
	kindsRepo db.KindsRepository
	ofs       *fakeOFS
	userID    uint // populated by registerAndLogin; copied onto each task in createTask
}

func newSmokeHarness(t *testing.T) *smokeHarness {
	t.Helper()

	// In-memory SQLite for the auth user table and the kinds (resource) table.
	// FK constraints are disabled at migrate time because Kind references User
	// and gorm's SQLite driver chokes on the cycle otherwise — the test never
	// exercises FK enforcement.
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gormDB.AutoMigrate(&db.User{}, &db.Kind{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	agent := &fakeAgentServer{sessionID: "sess-smoke-001"}
	upstream := httptest.NewServer(agent.handler())
	t.Cleanup(upstream.Close)

	ofs := newFakeOFS()
	kindsRepo := db.NewKindsRepository(gormDB)

	mgr := &fakeManager{
		upstreamURL: upstream.URL,
		kindsRepo:   kindsRepo,
		ofsReader:   ofs,
	}

	cfg := &config.Config{
		Auth: config.AuthConfig{
			SecretKey:       "smoke-secret",
			TokenTTLSeconds: 3600,
		},
		MySQL: config.MySQLConfig{DSN: "ignored-but-must-be-nonempty"},
	}

	store := task.NewMemoryRepository()
	router := NewRouter(RouterDeps{
		Store:      store,
		Manager:    mgr,
		CORSOrigin: "*",
		DB:         gormDB,
		UserRepo:   db.NewUserRepository(gormDB),
		Cfg:        cfg,
		KindsRepo:  kindsRepo,
		OFSWriter:  ofs,
	})

	h := &smokeHarness{
		t:         t,
		router:    router,
		upstream:  upstream,
		agent:     agent,
		manager:   mgr,
		gormDB:    gormDB,
		store:     store,
		kindsRepo: kindsRepo,
		ofs:       ofs,
	}

	h.registerAndLogin("smoker", "p4ssw0rd")
	return h
}

func (h *smokeHarness) registerAndLogin(username, password string) {
	h.t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":%q,"email":%q}`, username, password, username+"@local")
	rw := h.do(http.MethodPost, "/api/auth/register", body, "")
	if rw.Code != http.StatusCreated {
		h.t.Fatalf("register: expected 201, got %d body=%s", rw.Code, rw.Body)
	}
	var resp tokenResponse
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		h.t.Fatalf("decode register response: %v", err)
	}
	if resp.AccessToken == "" {
		h.t.Fatal("register returned empty access_token")
	}
	h.token = resp.AccessToken

	// MemoryRepository does not auto-populate Task.UserID from the auth user
	// (that wiring lives in MySQLRepository). Look up the row we just created
	// so createTask() can stamp the right UserID — required for the manager's
	// resource-injection guard `t.UserID != 0`.
	var u db.User
	if err := h.gormDB.Where("user_name = ?", username).First(&u).Error; err != nil {
		h.t.Fatalf("lookup user %q after register: %v", username, err)
	}
	h.userID = u.ID
	h.user = &u
}

// do issues an HTTP request through the router with optional bearer auth.
func (h *smokeHarness) do(method, path, body, contentType string) *httptest.ResponseRecorder {
	h.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	} else if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	rw := httptest.NewRecorder()
	h.router.ServeHTTP(rw, req)
	return rw
}

func (h *smokeHarness) doNoAuth(method, path, body string) *httptest.ResponseRecorder {
	saved := h.token
	h.token = ""
	defer func() { h.token = saved }()
	return h.do(method, path, body, "")
}

// createTask drives the full /api/tasks POST path and returns the new task ID.
func (h *smokeHarness) createTask() string {
	h.t.Helper()
	rw := h.do(http.MethodPost, "/api/tasks", `{"username":"ignored"}`, "")
	if rw.Code != http.StatusCreated {
		h.t.Fatalf("create task: expected 201, got %d body=%s", rw.Code, rw.Body)
	}
	var resp createTaskResponse
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		h.t.Fatalf("decode create response: %v", err)
	}
	if resp.ID == "" {
		h.t.Fatal("created task has empty ID")
	}
	// Stamp UserID directly on the in-memory task — see registerAndLogin.
	if t, _ := h.store.Get(context.Background(), resp.ID); t != nil {
		t.UserID = h.userID
	}
	return resp.ID
}

// ---- tests ----

// TestSmoke_FullMessagingFlow drives the full happy path: auth → create task →
// first message (creates session, fetches title) → second message (existing
// session) → permission decision → question response → delete.
func TestSmoke_FullMessagingFlow(t *testing.T) {
	h := newSmokeHarness(t)
	taskID := h.createTask()

	// Fresh task: GET should report state="pending".
	rw := h.do(http.MethodGet, "/api/tasks/"+taskID, "", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("get task: expected 200, got %d body=%s", rw.Code, rw.Body)
	}
	var got getTaskResponse
	json.NewDecoder(rw.Body).Decode(&got)
	if got.State != "pending" {
		t.Fatalf("expected state=pending before first message, got %q", got.State)
	}

	// First message: provisions sandbox, creates session, streams session.init.
	rw = h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"hello there"}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("first message: expected 200, got %d body=%s", rw.Code, rw.Body)
	}
	body := rw.Body.String()
	if !strings.Contains(body, "session.init") {
		t.Errorf("first message body missing session.init event: %q", body)
	}
	if !strings.Contains(body, "sess-smoke-001") {
		t.Errorf("first message body missing session id: %q", body)
	}

	if h.manager.provisions != 1 {
		t.Errorf("expected one provision, got %d", h.manager.provisions)
	}
	if h.agent.createCalls != 1 {
		t.Errorf("expected one POST /sessions, got %d", h.agent.createCalls)
	}

	// State after first message: session set + sandbox running ⇒ "active".
	rw = h.do(http.MethodGet, "/api/tasks/"+taskID, "", "")
	json.NewDecoder(rw.Body).Decode(&got)
	if got.State != "active" {
		t.Errorf("expected state=active after first message, got %q", got.State)
	}
	if got.SessionID != "sess-smoke-001" {
		t.Errorf("expected session_id=sess-smoke-001, got %q", got.SessionID)
	}
	if got.SandboxID != "sb-smoke" {
		t.Errorf("expected sandbox_id=sb-smoke, got %q", got.SandboxID)
	}
	if got.Title != "Smoke task" {
		t.Errorf("expected title=Smoke task, got %q", got.Title)
	}

	// Second message: same session, routed to /sessions/:id/messages, no re-provision.
	rw = h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"more"}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("second message: expected 200, got %d body=%s", rw.Code, rw.Body)
	}
	if !strings.Contains(rw.Body.String(), "second reply") {
		t.Errorf("second message body missing forwarded data: %q", rw.Body.String())
	}
	if h.manager.provisions != 1 {
		t.Errorf("second message should not re-provision, provisions=%d", h.manager.provisions)
	}
	if h.agent.messageCalls != 1 {
		t.Errorf("expected one POST /sessions/:id/messages, got %d", h.agent.messageCalls)
	}
	wantPath := "/sessions/sess-smoke-001/messages"
	if h.agent.lastMessageURL != wantPath {
		t.Errorf("expected message URL %q, got %q", wantPath, h.agent.lastMessageURL)
	}

	// Permission response: forwarded to /sessions/:id/permissions/respond.
	rw = h.do(http.MethodPost, "/api/tasks/"+taskID+"/permissions", `{"decision":"allow"}`, "")
	if rw.Code != http.StatusNoContent {
		t.Fatalf("permission: expected 204, got %d body=%s", rw.Code, rw.Body)
	}
	if !strings.Contains(h.agent.permissionBody, `"decision":"allow"`) {
		t.Errorf("upstream permission body = %q, want decision=allow", h.agent.permissionBody)
	}

	// Question response: forwarded to /sessions/:id/questions/respond.
	rw = h.do(http.MethodPost, "/api/tasks/"+taskID+"/questions",
		`{"answers":{"q1":"yes","q2":42}}`, "")
	if rw.Code != http.StatusNoContent {
		t.Fatalf("question: expected 204, got %d body=%s", rw.Code, rw.Body)
	}
	if !strings.Contains(h.agent.questionBody, `"answers"`) ||
		!strings.Contains(h.agent.questionBody, `"q1":"yes"`) {
		t.Errorf("upstream question body = %q, want answers payload", h.agent.questionBody)
	}

	// Delete: 204 and DeleteSandbox is called.
	rw = h.do(http.MethodDelete, "/api/tasks/"+taskID, "", "")
	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d body=%s", rw.Code, rw.Body)
	}
	if h.manager.deletes != 1 {
		t.Errorf("expected one DeleteSandbox, got %d", h.manager.deletes)
	}

	// Subsequent GET should 404.
	rw = h.do(http.MethodGet, "/api/tasks/"+taskID, "", "")
	if rw.Code != http.StatusNotFound {
		t.Errorf("get after delete: expected 404, got %d", rw.Code)
	}
}

// TestSmoke_AuthRequired verifies that the protected messaging endpoints reject
// requests without a bearer token.
func TestSmoke_AuthRequired(t *testing.T) {
	h := newSmokeHarness(t)

	endpoints := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/tasks", `{"username":"x"}`},
		{http.MethodGet, "/api/tasks/anything", ""},
		{http.MethodPost, "/api/tasks/anything/messages", `{"prompt":"hi"}`},
		{http.MethodPost, "/api/tasks/anything/permissions", `{"decision":"allow"}`},
		{http.MethodPost, "/api/tasks/anything/questions", `{"answers":{"q":"a"}}`},
		{http.MethodDelete, "/api/tasks/anything", ""},
	}

	for _, ep := range endpoints {
		rw := h.doNoAuth(ep.method, ep.path, ep.body)
		if rw.Code != http.StatusUnauthorized {
			t.Errorf("%s %s no-auth: expected 401, got %d", ep.method, ep.path, rw.Code)
		}
	}
}

// TestSmoke_OwnershipEnforced verifies a user cannot read or message another
// user's task even with a valid token.
func TestSmoke_OwnershipEnforced(t *testing.T) {
	h := newSmokeHarness(t)
	taskID := h.createTask()

	// Register a second user and switch to their token.
	body := `{"username":"intruder","password":"p4ssw0rd","email":"i@local"}`
	rw := h.do(http.MethodPost, "/api/auth/register", body, "")
	if rw.Code != http.StatusCreated {
		t.Fatalf("register intruder: expected 201, got %d body=%s", rw.Code, rw.Body)
	}
	var tok tokenResponse
	json.NewDecoder(rw.Body).Decode(&tok)
	h.token = tok.AccessToken

	// GET on someone else's task → 403.
	rw = h.do(http.MethodGet, "/api/tasks/"+taskID, "", "")
	if rw.Code != http.StatusForbidden {
		t.Errorf("intruder GET: expected 403, got %d body=%s", rw.Code, rw.Body)
	}

	// POST messages on someone else's task → 403.
	rw = h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"hi"}`, "")
	if rw.Code != http.StatusForbidden {
		t.Errorf("intruder send: expected 403, got %d body=%s", rw.Code, rw.Body)
	}
}

// TestSmoke_ProvisionFailure_ReturnsBadGateway verifies that a provisioning
// failure surfaces as 502 and leaves the task in error state.
func TestSmoke_ProvisionFailure_ReturnsBadGateway(t *testing.T) {
	h := newSmokeHarness(t)
	h.manager.provisionErr = fmt.Errorf("upstream quota exceeded")

	taskID := h.createTask()

	rw := h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"hi"}`, "")
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on provision failure, got %d body=%s", rw.Code, rw.Body)
	}

	// Task state should reflect the error.
	rw = h.do(http.MethodGet, "/api/tasks/"+taskID, "", "")
	var got getTaskResponse
	json.NewDecoder(rw.Body).Decode(&got)
	if got.State != "error" {
		t.Errorf("expected state=error after provision failure, got %q", got.State)
	}
}

// TestSmoke_ListTasks_ScopedToUser verifies List returns only the caller's tasks.
func TestSmoke_ListTasks_ScopedToUser(t *testing.T) {
	h := newSmokeHarness(t)

	id1 := h.createTask()
	id2 := h.createTask()

	rw := h.do(http.MethodGet, "/api/tasks", "", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d body=%s", rw.Code, rw.Body)
	}
	var items []taskListItem
	if err := json.NewDecoder(rw.Body).Decode(&items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.ID] = true
	}
	if !seen[id1] || !seen[id2] {
		t.Errorf("list missing created tasks: have %v, want %s & %s", seen, id1, id2)
	}
}

// ---- resource injection smoke tests ----
//
// These tests cover the contract described in docs/specs/resources.md:
//
//   POST /api/resources writes content to OFS + creates a kinds row.
//   At sandbox provision time, Manager.injectResources lists active records
//   for t.UserID, fetches skill files from OFS, and writes them under
//   {taskCWD}/.claude/skills/{name}/, while composing all active MCP records
//   into a single {taskCWD}/.mcp.json.
//
// The fakeManager simulates the injection step (no real execd). It captures
// the exact (path → content) pairs it would have written so the tests can
// assert on the spec's path layout end-to-end through the real router, real
// resource handlers, real KindsRepository (sqlite), and real OFS (in-memory).

// createResource POSTs a resource definition through the real router and returns
// the new resource ID. The body is a fully-formed JSON string per the spec.
func (h *smokeHarness) createResource(body string) int {
	h.t.Helper()
	rw := h.do(http.MethodPost, "/api/resources", body, "")
	if rw.Code != http.StatusCreated {
		h.t.Fatalf("create resource: expected 201, got %d body=%s", rw.Code, rw.Body)
	}
	var resp resourceResponse
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		h.t.Fatalf("decode resource response: %v", err)
	}
	if resp.ID == 0 {
		h.t.Fatalf("created resource has zero ID: %s", rw.Body)
	}
	return resp.ID
}

// injectedFor returns the captured (path → content) pairs for the task's CWD.
func (h *smokeHarness) injectedFor(taskID string) map[string]string {
	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()
	prefix := fmt.Sprintf("/workspace/%s/%s/", h.user.UserName, taskID)
	out := map[string]string{}
	for k, v := range h.manager.injected {
		if rel, ok := strings.CutPrefix(k, prefix); ok {
			out[rel] = string(v)
		}
	}
	return out
}

// TestSmoke_ResourceInjection_Skill_SingleFile registers a skill via the API
// and verifies that on first message the manager injects SKILL.md at the
// spec'd sandbox path with the same content posted to OFS.
func TestSmoke_ResourceInjection_Skill_SingleFile(t *testing.T) {
	h := newSmokeHarness(t)
	h.createResource(`{"kind":"skill","name":"my-search","content":"# Search Skill\nUse this for queries."}`)

	taskID := h.createTask()

	// First message provisions the sandbox → injection runs.
	rw := h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"go"}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("first message: expected 200, got %d body=%s", rw.Code, rw.Body)
	}
	if h.manager.injectErr != nil {
		t.Fatalf("unexpected injection error: %v", h.manager.injectErr)
	}

	got := h.injectedFor(taskID)
	skillPath := ".claude/skills/my-search/SKILL.md"
	body, ok := got[skillPath]
	if !ok {
		t.Fatalf("expected injected file at %q, got keys %v", skillPath, mapKeys(got))
	}
	if body != "# Search Skill\nUse this for queries." {
		t.Errorf("injected SKILL.md content = %q", body)
	}
	if _, hasMCP := got[".mcp.json"]; hasMCP {
		t.Errorf("did not register an MCP — expected no .mcp.json injection, got: %s", got[".mcp.json"])
	}
}

// TestSmoke_ResourceInjection_Skill_MultiFile uploads a companion file via
// PUT /api/resources/:id/files/* and verifies the manager injects every entry
// in meta.files at the right relative sandbox path.
func TestSmoke_ResourceInjection_Skill_MultiFile(t *testing.T) {
	h := newSmokeHarness(t)
	id := h.createResource(`{"kind":"skill","name":"my-tool","content":"# My Tool"}`)

	// Upsert a companion file at scripts/helper.py.
	rw := h.do(http.MethodPut,
		fmt.Sprintf("/api/resources/%d/files/scripts/helper.py", id),
		"#!/usr/bin/env python3\nprint('hi')\n",
		"application/octet-stream")
	if rw.Code != http.StatusOK {
		t.Fatalf("upsert skill file: expected 200, got %d body=%s", rw.Code, rw.Body)
	}

	taskID := h.createTask()
	rw = h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"go"}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("first message: expected 200, got %d body=%s", rw.Code, rw.Body)
	}
	if h.manager.injectErr != nil {
		t.Fatalf("unexpected injection error: %v", h.manager.injectErr)
	}

	got := h.injectedFor(taskID)
	skill := got[".claude/skills/my-tool/SKILL.md"]
	if skill != "# My Tool" {
		t.Errorf("SKILL.md content = %q, want '# My Tool'", skill)
	}
	helper := got[".claude/skills/my-tool/scripts/helper.py"]
	if !strings.Contains(helper, "print('hi')") {
		t.Errorf("helper.py content = %q, want script body", helper)
	}
}

// TestSmoke_ResourceInjection_MCP registers an MCP server via the API and
// verifies the manager composes a single .mcp.json under the task CWD with
// the server entry keyed by the resource name.
func TestSmoke_ResourceInjection_MCP(t *testing.T) {
	h := newSmokeHarness(t)
	h.createResource(`{"kind":"mcp","name":"gh","meta":{"type":"stdio","command":"gh-mcp"}}`)
	h.createResource(`{"kind":"mcp","name":"jira","meta":{"type":"http","url":"https://j.example.com"}}`)

	taskID := h.createTask()
	rw := h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"go"}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("first message: expected 200, got %d body=%s", rw.Code, rw.Body)
	}

	got := h.injectedFor(taskID)
	raw, ok := got[".mcp.json"]
	if !ok {
		t.Fatalf("expected .mcp.json injection, got %v", mapKeys(got))
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode .mcp.json: %v (raw=%s)", err, raw)
	}
	if doc.MCPServers["gh"]["command"] != "gh-mcp" {
		t.Errorf(".mcp.json[gh].command = %v, want gh-mcp", doc.MCPServers["gh"]["command"])
	}
	if doc.MCPServers["jira"]["url"] != "https://j.example.com" {
		t.Errorf(".mcp.json[jira].url = %v, want https://j.example.com", doc.MCPServers["jira"]["url"])
	}
	// No skill registered → no SKILL.md injection.
	for k := range got {
		if strings.HasPrefix(k, ".claude/skills/") {
			t.Errorf("unexpected skill injection at %q without any skill registered", k)
		}
	}
}

// TestSmoke_ResourceInjection_InactiveSkipped flips a skill's is_active=false
// via PUT /api/resources/:id and verifies the manager does NOT inject it.
func TestSmoke_ResourceInjection_InactiveSkipped(t *testing.T) {
	h := newSmokeHarness(t)
	id := h.createResource(`{"kind":"skill","name":"silenced","content":"# Hidden"}`)

	rw := h.do(http.MethodPut, fmt.Sprintf("/api/resources/%d", id), `{"is_active":false}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("deactivate: expected 200, got %d body=%s", rw.Code, rw.Body)
	}

	taskID := h.createTask()
	rw = h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"go"}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("first message: expected 200, got %d body=%s", rw.Code, rw.Body)
	}

	got := h.injectedFor(taskID)
	if len(got) != 0 {
		t.Errorf("expected no injections for inactive-only resources, got %v", mapKeys(got))
	}
}

// TestSmoke_ResourceInjection_ScopedToOwner verifies that resources owned by
// one user are not injected when another user's task is provisioned. This is
// the per-user isolation the unique index (user_id, kind, name) hints at.
func TestSmoke_ResourceInjection_ScopedToOwner(t *testing.T) {
	h := newSmokeHarness(t)
	// User #1 (smoker, registered by the harness) owns a skill.
	h.createResource(`{"kind":"skill","name":"only-mine","content":"# Mine"}`)

	// Register and switch to a second user; create a task as that user.
	body := `{"username":"other","password":"p4ssw0rd","email":"o@local"}`
	rw := h.do(http.MethodPost, "/api/auth/register", body, "")
	if rw.Code != http.StatusCreated {
		t.Fatalf("register other: expected 201, got %d body=%s", rw.Code, rw.Body)
	}
	var tok tokenResponse
	json.NewDecoder(rw.Body).Decode(&tok)
	h.token = tok.AccessToken
	var u db.User
	if err := h.gormDB.Where("user_name = ?", "other").First(&u).Error; err != nil {
		t.Fatalf("lookup other user: %v", err)
	}
	prevUserID, prevUser := h.userID, h.user
	h.userID = u.ID
	h.user = &u
	defer func() { h.userID, h.user = prevUserID, prevUser }()

	taskID := h.createTask()
	rw = h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"go"}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("first message: expected 200, got %d body=%s", rw.Code, rw.Body)
	}

	got := h.injectedFor(taskID)
	if len(got) != 0 {
		t.Errorf("other-user task should not see smoker's resources, got injections: %v", mapKeys(got))
	}
}

// TestSmoke_ResourceInjection_OFSReadFailure_NonFatal verifies the spec's
// "injection failures are non-fatal" rule: when the OFS read fails, the task
// still reaches state=active (only an error is recorded internally).
func TestSmoke_ResourceInjection_OFSReadFailure_NonFatal(t *testing.T) {
	h := newSmokeHarness(t)
	h.createResource(`{"kind":"skill","name":"broken","content":"# Will be removed"}`)

	// Simulate OFS data loss for this skill's content. The DB record still
	// claims SKILL.md exists, so injection will fail at GetObjectBytes — but
	// per spec that must not block the provisioning flow.
	h.ofs.mu.Lock()
	delete(h.ofs.objects, "smoker/resources/skills/broken/SKILL.md")
	h.ofs.mu.Unlock()

	taskID := h.createTask()
	rw := h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"go"}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("first message: expected 200 despite injection failure, got %d body=%s", rw.Code, rw.Body)
	}
	if h.manager.injectErr == nil {
		t.Error("expected injection error to be recorded after OFS read failure")
	}

	// Task must still reach state=active — provisioning was not aborted.
	rw = h.do(http.MethodGet, "/api/tasks/"+taskID, "", "")
	var got getTaskResponse
	json.NewDecoder(rw.Body).Decode(&got)
	if got.State != "active" {
		t.Errorf("expected state=active after non-fatal injection failure, got %q", got.State)
	}
}

// TestSmoke_ResourceInjection_OFSContentMatchesAPIWrite asserts the round-trip
// invariant: bytes posted via POST /api/resources land in OFS unchanged, and
// the manager later reads back exactly those bytes during injection.
func TestSmoke_ResourceInjection_OFSContentMatchesAPIWrite(t *testing.T) {
	h := newSmokeHarness(t)
	skillBody := "# Round-trip\n\nLine two.\n"
	h.createResource(fmt.Sprintf(`{"kind":"skill","name":"rt","content":%q}`, skillBody))

	// Inspect what the API wrote into OFS — this is the contract boundary.
	h.ofs.mu.Lock()
	stored, ok := h.ofs.objects["smoker/resources/skills/rt/SKILL.md"]
	h.ofs.mu.Unlock()
	if !ok {
		t.Fatal("expected SKILL.md key in OFS after POST /api/resources")
	}
	if string(stored) != skillBody {
		t.Errorf("OFS-stored content = %q, want %q", stored, skillBody)
	}

	taskID := h.createTask()
	rw := h.do(http.MethodPost, "/api/tasks/"+taskID+"/messages", `{"prompt":"go"}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("first message: expected 200, got %d body=%s", rw.Code, rw.Body)
	}

	got := h.injectedFor(taskID)
	if got[".claude/skills/rt/SKILL.md"] != skillBody {
		t.Errorf("injected content = %q, want %q", got[".claude/skills/rt/SKILL.md"], skillBody)
	}
}

// TestSmoke_ResourceLifecycle_CRUD walks the resource API end-to-end through
// the real router: create → list → update meta → delete → list-empty.
func TestSmoke_ResourceLifecycle_CRUD(t *testing.T) {
	h := newSmokeHarness(t)

	skillID := h.createResource(`{"kind":"skill","name":"crud-sk","content":"# Hi"}`)
	mcpID := h.createResource(`{"kind":"mcp","name":"crud-mcp","meta":{"type":"stdio","command":"x"}}`)

	// List should return both, scoped to the caller.
	rw := h.do(http.MethodGet, "/api/resources", "", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("list resources: expected 200, got %d body=%s", rw.Code, rw.Body)
	}
	var items []resourceResponse
	if err := json.NewDecoder(rw.Body).Decode(&items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 resources, got %d: %+v", len(items), items)
	}

	// Update the MCP server config via meta-only PUT (no OFS write).
	rw = h.do(http.MethodPut, fmt.Sprintf("/api/resources/%d", mcpID),
		`{"meta":{"type":"http","url":"https://new.example.com"}}`, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("update mcp meta: expected 200, got %d body=%s", rw.Code, rw.Body)
	}
	var updated resourceResponse
	json.NewDecoder(rw.Body).Decode(&updated)
	if !strings.Contains(string(updated.Meta), "new.example.com") {
		t.Errorf("updated meta = %s, want url=new.example.com", updated.Meta)
	}

	// Delete the skill; list should now show only the MCP record.
	rw = h.do(http.MethodDelete, fmt.Sprintf("/api/resources/%d", skillID), "", "")
	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete skill: expected 204, got %d body=%s", rw.Code, rw.Body)
	}
	rw = h.do(http.MethodGet, "/api/resources", "", "")
	json.NewDecoder(rw.Body).Decode(&items)
	if len(items) != 1 || items[0].ID != mcpID {
		t.Errorf("expected only mcp left, got %+v", items)
	}
}

// mapKeys returns the keys of m as a slice (test helper for error messages).
func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
