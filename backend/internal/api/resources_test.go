package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/your-org/platform-backend/internal/auth"
	"github.com/your-org/platform-backend/internal/db"
)

// ---- resource mock types ----

type mockKindsRepo struct {
	createRec *db.KindRecord
	createErr error
	getRec    *db.KindRecord
	getErr    error
	listRecs  []*db.KindRecord
	listErr   error
	updateRec *db.KindRecord
	updateErr error
	deleteErr error

	capturedCreate struct {
		userID  uint
		kind    string
		name    string
		ofsPath string
		meta    json.RawMessage
	}
	capturedUpdate db.KindUpdate
}

func (m *mockKindsRepo) Create(_ context.Context, userID uint, kind, name, ofsPath string, meta json.RawMessage) (*db.KindRecord, error) {
	m.capturedCreate.userID = userID
	m.capturedCreate.kind = kind
	m.capturedCreate.name = name
	m.capturedCreate.ofsPath = ofsPath
	m.capturedCreate.meta = meta
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createRec != nil {
		return m.createRec, nil
	}
	return &db.KindRecord{
		ID: 1, Kind: kind, Name: name, OFSPath: ofsPath,
		Meta: meta, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, nil
}

func (m *mockKindsRepo) Get(_ context.Context, _ int, _ uint) (*db.KindRecord, error) {
	return m.getRec, m.getErr
}

func (m *mockKindsRepo) List(_ context.Context, _ uint) ([]*db.KindRecord, error) {
	return m.listRecs, m.listErr
}

func (m *mockKindsRepo) ListActive(_ context.Context, _ uint) ([]*db.KindRecord, error) {
	return m.listRecs, m.listErr
}

func (m *mockKindsRepo) Update(_ context.Context, _ int, _ uint, u db.KindUpdate) (*db.KindRecord, error) {
	m.capturedUpdate = u
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	if m.updateRec != nil {
		return m.updateRec, nil
	}
	return &db.KindRecord{ID: 1, Kind: "skill", Name: "x", Meta: json.RawMessage("{}"), IsActive: true}, nil
}

func (m *mockKindsRepo) Delete(_ context.Context, _ int, _ uint) error {
	return m.deleteErr
}

type mockOFSWriter struct {
	err          error
	capturedKey  string
	capturedData []byte
}

func (m *mockOFSWriter) PutObject(_ context.Context, key string, data []byte) error {
	m.capturedKey = key
	m.capturedData = data
	return m.err
}

type mockOFSReader struct {
	data map[string][]byte
	err  error
}

func (m *mockOFSReader) GetObjectBytes(_ context.Context, key string) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.data != nil {
		if v, ok := m.data[key]; ok {
			return v, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", key)
}

// ---- helpers ----

// resourceHandler builds a Handler with resource deps wired and returns it.
func resourceHandler(kr *mockKindsRepo, w *mockOFSWriter) *Handler {
	h := NewHandler(&mockStore{}, &mockManager{}, &mockProxy{}, nil)
	if kr != nil {
		h.withResources(kr, w, &mockOFSReader{})
	}
	return h
}

// authedContext creates a Gin context with a test user attached and the given request.
func authedContext(method, path, body string) (*httptest.ResponseRecorder, *gin.Context) {
	rw := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rw)
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	c.Request = httptest.NewRequest(method, path, bodyReader)
	auth.SetUser(c, &db.User{ID: 1, UserName: "alice"})
	return rw, c
}

// ---- CreateResource ----

func TestCreateResource_NotConfigured(t *testing.T) {
	h := resourceHandler(nil, nil)
	rw, c := authedContext(http.MethodPost, "/api/resources", `{"kind":"skill","name":"s","content":"x"}`)
	h.CreateResource(c)
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rw.Code)
	}
}

func TestCreateResource_Unauthenticated(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rw)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/resources", strings.NewReader(`{"kind":"skill","name":"s","content":"x"}`))
	// no auth.SetUser
	h.CreateResource(c)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rw.Code)
	}
}

func TestCreateResource_Skill_Success(t *testing.T) {
	kr := &mockKindsRepo{}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPost, "/api/resources", `{"kind":"skill","name":"my-sk","content":"# Skill"}`)
	h.CreateResource(c)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rw.Code, rw.Body)
	}
	if w.capturedKey != "alice/resources/skills/my-sk/SKILL.md" {
		t.Errorf("OFS key = %q, want alice/resources/skills/my-sk/SKILL.md", w.capturedKey)
	}
	if string(w.capturedData) != "# Skill" {
		t.Errorf("OFS data = %q, want '# Skill'", w.capturedData)
	}
	if kr.capturedCreate.kind != "skill" {
		t.Errorf("Create kind = %q, want skill", kr.capturedCreate.kind)
	}
	if kr.capturedCreate.name != "my-sk" {
		t.Errorf("Create name = %q, want my-sk", kr.capturedCreate.name)
	}
	if kr.capturedCreate.ofsPath != "alice/resources/skills/my-sk/" {
		t.Errorf("Create ofsPath = %q", kr.capturedCreate.ofsPath)
	}
	// meta must be initialized with the files manifest
	var meta db.SkillMeta
	if err := json.Unmarshal(kr.capturedCreate.meta, &meta); err != nil {
		t.Fatalf("meta is not valid JSON: %v", err)
	}
	if len(meta.Files) != 1 || meta.Files[0] != "SKILL.md" {
		t.Errorf("meta.files = %v, want [SKILL.md]", meta.Files)
	}

	var resp map[string]any
	json.NewDecoder(rw.Body).Decode(&resp)
	if resp["kind"] != "skill" {
		t.Errorf("response kind = %v", resp["kind"])
	}
}

func TestCreateResource_Skill_EmptyContent_Rejected(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPost, "/api/resources", `{"kind":"skill","name":"my-sk"}`)
	h.CreateResource(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing skill content, got %d", rw.Code)
	}
}

func TestCreateResource_MCP_Success(t *testing.T) {
	kr := &mockKindsRepo{}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPost, "/api/resources",
		`{"kind":"mcp","name":"gh","meta":{"type":"stdio","command":"npx"}}`)
	h.CreateResource(c)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rw.Code, rw.Body)
	}
	if w.capturedKey != "alice/resources/mcp/gh.json" {
		t.Errorf("OFS key = %q, want alice/resources/mcp/gh.json", w.capturedKey)
	}
	if kr.capturedCreate.ofsPath != "alice/resources/mcp/gh.json" {
		t.Errorf("Create ofsPath = %q", kr.capturedCreate.ofsPath)
	}
	if !json.Valid(kr.capturedCreate.meta) {
		t.Errorf("Create meta is not valid JSON: %s", kr.capturedCreate.meta)
	}
}

func TestCreateResource_MCP_ContentFallback(t *testing.T) {
	kr := &mockKindsRepo{}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPost, "/api/resources",
		`{"kind":"mcp","name":"srv","content":"{\"type\":\"stdio\"}"}`)
	h.CreateResource(c)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rw.Code, rw.Body)
	}
	if !json.Valid(kr.capturedCreate.meta) {
		t.Errorf("meta derived from content should be valid JSON: %s", kr.capturedCreate.meta)
	}
}

func TestCreateResource_InvalidKind(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPost, "/api/resources", `{"kind":"agent","name":"x","content":"y"}`)
	h.CreateResource(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown kind, got %d", rw.Code)
	}
}

func TestCreateResource_InvalidName_Spaces(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPost, "/api/resources", `{"kind":"skill","name":"my skill","content":"x"}`)
	h.CreateResource(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for name with spaces, got %d", rw.Code)
	}
}

func TestCreateResource_InvalidName_Slash(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPost, "/api/resources", `{"kind":"skill","name":"a/b","content":"x"}`)
	h.CreateResource(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for name with slash, got %d", rw.Code)
	}
}

func TestCreateResource_MCP_InvalidJSON(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPost, "/api/resources", `{"kind":"mcp","name":"x","content":"not json"}`)
	h.CreateResource(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON MCP content, got %d", rw.Code)
	}
}

func TestCreateResource_OFSError(t *testing.T) {
	kr := &mockKindsRepo{}
	w := &mockOFSWriter{err: errTest}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPost, "/api/resources", `{"kind":"skill","name":"s","content":"x"}`)
	h.CreateResource(c)
	if rw.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on OFS error, got %d", rw.Code)
	}
}

func TestCreateResource_RepoError(t *testing.T) {
	kr := &mockKindsRepo{createErr: errTest}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPost, "/api/resources", `{"kind":"skill","name":"s","content":"x"}`)
	h.CreateResource(c)
	if rw.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on repo error, got %d", rw.Code)
	}
}

// ---- ListResources ----

func TestListResources_NotConfigured(t *testing.T) {
	h := resourceHandler(nil, nil)
	rw, c := authedContext(http.MethodGet, "/api/resources", "")
	h.ListResources(c)
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rw.Code)
	}
}

func TestListResources_Empty(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{listRecs: []*db.KindRecord{}}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodGet, "/api/resources", "")
	h.ListResources(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	var items []any
	json.NewDecoder(rw.Body).Decode(&items)
	if len(items) != 0 {
		t.Errorf("expected empty array, got %d items", len(items))
	}
}

func TestListResources_WithItems(t *testing.T) {
	recs := []*db.KindRecord{
		{ID: 1, Kind: "skill", Name: "s1", OFSPath: "alice/resources/skills/s1/", Meta: json.RawMessage("{}"), IsActive: true},
		{ID: 2, Kind: "mcp", Name: "m1", OFSPath: "alice/resources/mcp/m1.json", Meta: json.RawMessage(`{"type":"stdio"}`), IsActive: true},
	}
	h := resourceHandler(&mockKindsRepo{listRecs: recs}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodGet, "/api/resources", "")
	h.ListResources(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	var items []map[string]any
	json.NewDecoder(rw.Body).Decode(&items)
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
	if items[0]["kind"] != "skill" {
		t.Errorf("item[0].kind = %v, want skill", items[0]["kind"])
	}
}

func TestListResources_RepoError(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{listErr: errTest}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodGet, "/api/resources", "")
	h.ListResources(c)
	if rw.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on repo error, got %d", rw.Code)
	}
}

// ---- UpdateResource ----

func TestUpdateResource_NotConfigured(t *testing.T) {
	h := resourceHandler(nil, nil)
	rw, c := authedContext(http.MethodPut, "/api/resources/1", `{"is_active":false}`)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.UpdateResource(c)
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rw.Code)
	}
}

func TestUpdateResource_InvalidID(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPut, "/api/resources/abc", `{"is_active":true}`)
	c.Params = gin.Params{{Key: "id", Value: "abc"}}
	h.UpdateResource(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-numeric id, got %d", rw.Code)
	}
}

func TestUpdateResource_Skill_Content(t *testing.T) {
	skillRec := &db.KindRecord{
		ID: 1, Kind: "skill", Name: "my-sk",
		OFSPath: "alice/resources/skills/my-sk/",
		Meta:    json.RawMessage("{}"), IsActive: true,
	}
	kr := &mockKindsRepo{getRec: skillRec}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPut, "/api/resources/1", `{"content":"# Updated Skill"}`)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.UpdateResource(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body)
	}
	if w.capturedKey != "alice/resources/skills/my-sk/SKILL.md" {
		t.Errorf("OFS key = %q, want alice/resources/skills/my-sk/SKILL.md", w.capturedKey)
	}
	if string(w.capturedData) != "# Updated Skill" {
		t.Errorf("OFS data = %q", w.capturedData)
	}
	// For skill content update, Meta in KindUpdate should not be set by the content field.
	if kr.capturedUpdate.Meta != nil {
		t.Errorf("expected nil Meta in KindUpdate for skill content update, got %s", kr.capturedUpdate.Meta)
	}
}

func TestUpdateResource_MCP_Content(t *testing.T) {
	mcpRec := &db.KindRecord{
		ID: 2, Kind: "mcp", Name: "gh",
		OFSPath: "alice/resources/mcp/gh.json",
		Meta:    json.RawMessage(`{"type":"stdio"}`), IsActive: true,
	}
	kr := &mockKindsRepo{getRec: mcpRec}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPut, "/api/resources/2",
		`{"content":"{\"type\":\"http\",\"url\":\"https://api.example.com\"}"}`)
	c.Params = gin.Params{{Key: "id", Value: "2"}}
	h.UpdateResource(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body)
	}
	if w.capturedKey != "alice/resources/mcp/gh.json" {
		t.Errorf("OFS key = %q, want alice/resources/mcp/gh.json", w.capturedKey)
	}
	if kr.capturedUpdate.Meta == nil {
		t.Error("expected Meta set in KindUpdate for mcp content update")
	}
}

func TestUpdateResource_MCP_InvalidJSON(t *testing.T) {
	mcpRec := &db.KindRecord{
		ID: 2, Kind: "mcp", Name: "gh",
		OFSPath: "alice/resources/mcp/gh.json",
		Meta:    json.RawMessage(`{}`), IsActive: true,
	}
	kr := &mockKindsRepo{getRec: mcpRec}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPut, "/api/resources/2", `{"content":"not json"}`)
	c.Params = gin.Params{{Key: "id", Value: "2"}}
	h.UpdateResource(c)

	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid MCP JSON, got %d", rw.Code)
	}
}

func TestUpdateResource_IsActive(t *testing.T) {
	kr := &mockKindsRepo{}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPut, "/api/resources/1", `{"is_active":false}`)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.UpdateResource(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body)
	}
	if w.capturedKey != "" {
		t.Error("is_active update should not touch OFS")
	}
	if kr.capturedUpdate.IsActive == nil || *kr.capturedUpdate.IsActive {
		t.Error("expected IsActive=false in KindUpdate")
	}
}

func TestUpdateResource_MetaOnly(t *testing.T) {
	kr := &mockKindsRepo{}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)

	rw, c := authedContext(http.MethodPut, "/api/resources/1", `{"meta":{"key":"val"}}`)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.UpdateResource(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body)
	}
	if w.capturedKey != "" {
		t.Error("meta-only update should not touch OFS")
	}
	if kr.capturedUpdate.Meta == nil {
		t.Error("expected Meta set in KindUpdate")
	}
}

func TestUpdateResource_NotFound_ContentGet(t *testing.T) {
	kr := &mockKindsRepo{getErr: errTest}
	h := resourceHandler(kr, &mockOFSWriter{})

	rw, c := authedContext(http.MethodPut, "/api/resources/1", `{"content":"new text"}`)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.UpdateResource(c)

	if rw.Code != http.StatusNotFound {
		t.Errorf("expected 404 when Get fails, got %d", rw.Code)
	}
}

func TestUpdateResource_RepoError(t *testing.T) {
	kr := &mockKindsRepo{updateErr: errTest}
	h := resourceHandler(kr, &mockOFSWriter{})

	rw, c := authedContext(http.MethodPut, "/api/resources/1", `{"is_active":true}`)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.UpdateResource(c)

	if rw.Code != http.StatusNotFound {
		t.Errorf("expected 404 when Update fails, got %d", rw.Code)
	}
}

// ---- DeleteResource ----

func TestDeleteResource_NotConfigured(t *testing.T) {
	h := resourceHandler(nil, nil)
	rw, c := authedContext(http.MethodDelete, "/api/resources/1", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.DeleteResource(c)
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rw.Code)
	}
}

func TestDeleteResource_InvalidID(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodDelete, "/api/resources/xyz", "")
	c.Params = gin.Params{{Key: "id", Value: "xyz"}}
	h.DeleteResource(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-numeric id, got %d", rw.Code)
	}
}

func TestDeleteResource_Success(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodDelete, "/api/resources/1", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.DeleteResource(c)
	if rw.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rw.Code)
	}
}

func TestDeleteResource_NotFound(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{deleteErr: errTest}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodDelete, "/api/resources/1", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.DeleteResource(c)
	if rw.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rw.Code)
	}
}

// ---- UpsertSkillFile ----

func skillRecord() *db.KindRecord {
	meta, _ := json.Marshal(db.SkillMeta{Files: []string{"SKILL.md"}})
	return &db.KindRecord{
		ID: 1, Kind: "skill", Name: "my-sk",
		OFSPath: "alice/resources/skills/my-sk/",
		Meta:    meta, IsActive: true,
	}
}

func TestUpsertSkillFile_NotConfigured(t *testing.T) {
	h := resourceHandler(nil, nil)
	rw, c := authedContext(http.MethodPut, "/api/resources/1/files/scripts/helper.py", "#!/usr/bin/env python3")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/scripts/helper.py"}}
	h.UpsertSkillFile(c)
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rw.Code)
	}
}

func TestUpsertSkillFile_InvalidID(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPut, "/api/resources/abc/files/SKILL.md", "# hi")
	c.Params = gin.Params{{Key: "id", Value: "abc"}, {Key: "filepath", Value: "/SKILL.md"}}
	h.UpsertSkillFile(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-numeric id, got %d", rw.Code)
	}
}

func TestUpsertSkillFile_InvalidPath_DotDot(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPut, "/api/resources/1/files/../etc/passwd", "x")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/../etc/passwd"}}
	h.UpsertSkillFile(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for path traversal, got %d", rw.Code)
	}
}

func TestUpsertSkillFile_FileTooLarge(t *testing.T) {
	kr := &mockKindsRepo{getRec: skillRecord()}
	h := resourceHandler(kr, &mockOFSWriter{})
	bigBody := strings.Repeat("x", maxSkillFileSize+1)
	rw, c := authedContext(http.MethodPut, "/api/resources/1/files/big.txt", bigBody)
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/big.txt"}}
	h.UpsertSkillFile(c)
	if rw.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for oversized file, got %d", rw.Code)
	}
}

func TestUpsertSkillFile_NotFound(t *testing.T) {
	kr := &mockKindsRepo{getErr: errTest}
	h := resourceHandler(kr, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPut, "/api/resources/1/files/helper.py", "code")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/helper.py"}}
	h.UpsertSkillFile(c)
	if rw.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rw.Code)
	}
}

func TestUpsertSkillFile_NotSkillKind(t *testing.T) {
	mcpRec := &db.KindRecord{ID: 1, Kind: "mcp", Name: "gh", OFSPath: "alice/resources/mcp/gh.json", Meta: json.RawMessage(`{}`)}
	kr := &mockKindsRepo{getRec: mcpRec}
	h := resourceHandler(kr, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPut, "/api/resources/1/files/helper.py", "code")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/helper.py"}}
	h.UpsertSkillFile(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-skill kind, got %d", rw.Code)
	}
}

func TestUpsertSkillFile_ExceedMaxFiles(t *testing.T) {
	files := make([]string, maxSkillFiles)
	for i := range files {
		files[i] = fmt.Sprintf("file%d.txt", i)
	}
	fullMeta, _ := json.Marshal(db.SkillMeta{Files: files})
	rec := &db.KindRecord{ID: 1, Kind: "skill", Name: "sk", OFSPath: "alice/resources/skills/sk/", Meta: fullMeta}
	kr := &mockKindsRepo{getRec: rec}
	h := resourceHandler(kr, &mockOFSWriter{})
	rw, c := authedContext(http.MethodPut, "/api/resources/1/files/extra.txt", "x")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/extra.txt"}}
	h.UpsertSkillFile(c)
	if rw.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 when exceeding max files, got %d", rw.Code)
	}
}

func TestUpsertSkillFile_NewFile_UpdatesMeta(t *testing.T) {
	kr := &mockKindsRepo{getRec: skillRecord()}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)
	rw, c := authedContext(http.MethodPut, "/api/resources/1/files/scripts/helper.py", "#!/usr/bin/env python3")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/scripts/helper.py"}}
	h.UpsertSkillFile(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body)
	}
	if w.capturedKey != "alice/resources/skills/my-sk/scripts/helper.py" {
		t.Errorf("OFS key = %q", w.capturedKey)
	}
	var meta db.SkillMeta
	json.Unmarshal(kr.capturedUpdate.Meta, &meta)
	if len(meta.Files) != 2 || meta.Files[1] != "scripts/helper.py" {
		t.Errorf("updated meta.files = %v, want [SKILL.md scripts/helper.py]", meta.Files)
	}
}

func TestUpsertSkillFile_ExistingFile_NoMetaUpdate(t *testing.T) {
	meta, _ := json.Marshal(db.SkillMeta{Files: []string{"SKILL.md", "helper.py"}})
	rec := &db.KindRecord{ID: 1, Kind: "skill", Name: "sk", OFSPath: "alice/resources/skills/sk/", Meta: meta}
	kr := &mockKindsRepo{getRec: rec}
	w := &mockOFSWriter{}
	h := resourceHandler(kr, w)
	rw, c := authedContext(http.MethodPut, "/api/resources/1/files/helper.py", "updated code")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/helper.py"}}
	h.UpsertSkillFile(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body)
	}
	if w.capturedKey != "alice/resources/skills/sk/helper.py" {
		t.Errorf("OFS key = %q", w.capturedKey)
	}
	// existing file: repo Update must NOT be called (meta unchanged)
	if kr.capturedUpdate.Meta != nil {
		t.Errorf("expected no meta update for existing file, got %s", kr.capturedUpdate.Meta)
	}
}

func TestUpsertSkillFile_OFSError(t *testing.T) {
	kr := &mockKindsRepo{getRec: skillRecord()}
	w := &mockOFSWriter{err: errTest}
	h := resourceHandler(kr, w)
	rw, c := authedContext(http.MethodPut, "/api/resources/1/files/helper.py", "code")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/helper.py"}}
	h.UpsertSkillFile(c)
	if rw.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on OFS error, got %d", rw.Code)
	}
}

// ---- DeleteSkillFile ----

func TestDeleteSkillFile_NotConfigured(t *testing.T) {
	h := resourceHandler(nil, nil)
	rw, c := authedContext(http.MethodDelete, "/api/resources/1/files/helper.py", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/helper.py"}}
	h.DeleteSkillFile(c)
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rw.Code)
	}
}

func TestDeleteSkillFile_CannotRemoveSKILLMD(t *testing.T) {
	h := resourceHandler(&mockKindsRepo{}, &mockOFSWriter{})
	rw, c := authedContext(http.MethodDelete, "/api/resources/1/files/SKILL.md", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/SKILL.md"}}
	h.DeleteSkillFile(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when removing SKILL.md, got %d", rw.Code)
	}
}

func TestDeleteSkillFile_FileNotInManifest(t *testing.T) {
	kr := &mockKindsRepo{getRec: skillRecord()}
	h := resourceHandler(kr, &mockOFSWriter{})
	rw, c := authedContext(http.MethodDelete, "/api/resources/1/files/nonexistent.py", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/nonexistent.py"}}
	h.DeleteSkillFile(c)
	if rw.Code != http.StatusNotFound {
		t.Errorf("expected 404 for file not in manifest, got %d", rw.Code)
	}
}

func TestDeleteSkillFile_Success(t *testing.T) {
	meta, _ := json.Marshal(db.SkillMeta{Files: []string{"SKILL.md", "helper.py"}})
	rec := &db.KindRecord{ID: 1, Kind: "skill", Name: "sk", OFSPath: "alice/resources/skills/sk/", Meta: meta}
	kr := &mockKindsRepo{getRec: rec}
	h := resourceHandler(kr, &mockOFSWriter{})
	rw, c := authedContext(http.MethodDelete, "/api/resources/1/files/helper.py", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/helper.py"}}
	h.DeleteSkillFile(c)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body)
	}
	var updatedMeta db.SkillMeta
	json.Unmarshal(kr.capturedUpdate.Meta, &updatedMeta)
	if len(updatedMeta.Files) != 1 || updatedMeta.Files[0] != "SKILL.md" {
		t.Errorf("after delete, meta.files = %v, want [SKILL.md]", updatedMeta.Files)
	}
}

func TestDeleteSkillFile_NotSkillKind(t *testing.T) {
	mcpRec := &db.KindRecord{ID: 1, Kind: "mcp", Name: "gh", OFSPath: "alice/resources/mcp/gh.json", Meta: json.RawMessage(`{}`)}
	kr := &mockKindsRepo{getRec: mcpRec}
	h := resourceHandler(kr, &mockOFSWriter{})
	rw, c := authedContext(http.MethodDelete, "/api/resources/1/files/helper.py", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "filepath", Value: "/helper.py"}}
	h.DeleteSkillFile(c)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-skill kind, got %d", rw.Code)
	}
}

// errTest is a sentinel error for mock failures.
var errTest = errorString("test error")

type errorString string

func (e errorString) Error() string { return string(e) }

// compile-time checks
var _ db.KindsRepository = (*mockKindsRepo)(nil)
var _ ResourceWriter = (*mockOFSWriter)(nil)
