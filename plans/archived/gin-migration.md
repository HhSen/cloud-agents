# Plan: Migrate `internal/api` from `net/http` to Gin

## Scope

Affects only the `internal/api` package and `cmd/server/main.go`.
No changes to domain logic, stores, managers, proxy, or public interfaces.

---

## Files changed

| File | Nature of change |
|---|---|
| `go.mod` / `go.sum` | Add `github.com/gin-gonic/gin` |
| `internal/api/router.go` | Replace ServeMux + hand-rolled CORS with Gin engine + CORS middleware |
| `internal/api/handlers.go` | Change handler signatures from `(w, r)` to `(*gin.Context)`; replace all manual JSON decoding with `c.ShouldBindJSON` |
| `internal/api/types.go` | Add `binding:"required"` to `sendMessageRequest.Prompt` |
| `internal/api/handlers_test.go` | Update test setup to use `gin.CreateTestContext`; update `CreateTask` no-body/invalid-JSON tests to expect 400 |
| `internal/api/router_test.go` | Minimal changes — tests already go through `router.ServeHTTP` |
| `cmd/server/main.go` | Optionally use `engine.Run()` instead of `http.ListenAndServe` |

---

## Step-by-step

### 1. Add the dependency

```bash
cd backend && go get github.com/gin-gonic/gin
```

### 2. `router.go` — replace ServeMux with Gin engine

**Before**
```go
func NewRouter(store TaskStore, mgr SandboxManager, corsOrigin string, fileStore FileStore) http.Handler {
    h := NewHandler(store, mgr, sandbox.NewProxy(), fileStore)
    mux := http.NewServeMux()
    mux.HandleFunc("POST /api/tasks", h.CreateTask)
    mux.HandleFunc("POST /api/tasks/{id}/messages", h.SendMessage)
    ...
    return corsMiddleware(corsOrigin, mux)
}
```

**After**
```go
func NewRouter(store TaskStore, mgr SandboxManager, corsOrigin string, fileStore FileStore) http.Handler {
    h := NewHandler(store, mgr, sandbox.NewProxy(), fileStore)

    r := gin.New()
    r.Use(gin.Recovery())
    r.Use(corsMiddleware(corsOrigin))

    r.POST("/api/tasks", h.CreateTask)
    r.POST("/api/tasks/:id/messages", h.SendMessage)
    r.GET("/api/tasks/:id", h.GetTask)
    r.GET("/api/tasks/:id/history", h.GetTaskHistory)
    r.DELETE("/api/tasks/:id", h.DeleteTask)
    r.GET("/health", h.Health)

    return r
}

func corsMiddleware(origin string) gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("Access-Control-Allow-Origin", origin)
        c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
        c.Header("Access-Control-Allow-Headers", "Content-Type")
        if c.Request.Method == http.MethodOptions {
            c.AbortWithStatus(http.StatusNoContent)
            return
        }
        c.Next()
    }
}
```

**Note:** `gin.New()` + `gin.Recovery()` is preferred over `gin.Default()` to avoid Gin's built-in logger conflicting with the existing `log.Printf` calls.

### 3. `handlers.go` — update all handler signatures

**Signature change (all 5 handlers)**
```go
// Before
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request)

// After
func (h *Handler) CreateTask(c *gin.Context)
```

**Mechanical substitutions per handler**

| Before | After |
|---|---|
| `r.PathValue("id")` | `c.Param("id")` |
| `r.Context()` | `c.Request.Context()` |
| `json.NewDecoder(r.Body).Decode(&body)` | `c.ShouldBindJSON(&body)` |
| `http.Error(w, msg, code)` | `c.String(code, msg)` |
| `w.Header().Set("Content-Type", "application/json")` + `w.WriteHeader(code)` + `json.NewEncoder(w).Encode(v)` | `c.JSON(code, v)` |
| `w.WriteHeader(http.StatusNoContent)` | `c.Status(http.StatusNoContent)` |
| `w` passed to `proxy.StreamMessage(...)` | `c.Writer` (implements `http.ResponseWriter`) |

**Handler-by-handler notes**

- **`CreateTask`**: use `c.ShouldBindJSON(&body)`; return 400 on error. Replace `w.WriteHeader(201)` + `json.NewEncoder(w).Encode(...)` with `c.JSON(201, createTaskResponse{...})`.

  ```go
  func (h *Handler) CreateTask(c *gin.Context) {
      var body createTaskRequest
      if err := c.ShouldBindJSON(&body); err != nil {
          c.String(http.StatusBadRequest, "invalid request body")
          return
      }
      t, err := h.store.Create(c.Request.Context(), body.Username, body.Env)
      if err != nil {
          log.Printf("create task: %v", err)
          c.String(http.StatusInternalServerError, "failed to create task")
          return
      }
      c.JSON(http.StatusCreated, createTaskResponse{ID: t.ID})
  }
  ```

- **`SendMessage`**: replace manual `json.NewDecoder(r.Body).Decode` + `body.Prompt == ""` check with `c.ShouldBindJSON(&body)`. The empty/missing prompt validation moves to a `binding:"required"` tag on `sendMessageRequest.Prompt` (see `types.go` change below). Pass `c.Writer` to `proxy.StreamMessage` and `c.Request.Context()` everywhere a context is needed. The `context.Background()` provisioning path is unchanged.

  ```go
  var body sendMessageRequest
  if err := c.ShouldBindJSON(&body); err != nil {
      c.String(http.StatusBadRequest, "prompt is required")
      return
  }
  ```

- **`GetTask`**, **`GetTaskHistory`**, **`DeleteTask`**: straightforward substitutions from the table above.

- **`Health`**: `c.JSON(200, healthResponse{Status: "ok"})`.

- **`MessageProxy` interface** — `StreamMessage(..., w http.ResponseWriter)` does **not** change; `c.Writer` satisfies it.

### 4. `handlers_test.go` — adapt unit tests

The tests call handler methods directly. With Gin, the idiomatic way to test a handler in isolation is `gin.CreateTestContext`:

```go
// Before
req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+tsk.ID, nil)
req.SetPathValue("id", tsk.ID)
rw := httptest.NewRecorder()
h.GetTask(rw, req)

// After
rw := httptest.NewRecorder()
c, _ := gin.CreateTestContext(rw)
c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/"+tsk.ID, nil)
c.Params = gin.Params{{Key: "id", Value: tsk.ID}}
h.GetTask(c)

if rw.Code != http.StatusOK { ... }
```

Apply this pattern to every test that calls a handler method directly. Tests that pass a body use `strings.NewReader(...)` on `c.Request` unchanged.

**`CreateTask` test changes** — two existing tests must be updated because `CreateTask` now rejects missing/malformed bodies:

| Test | Old expectation | New expectation |
|---|---|---|
| `TestCreateTask_NoBody` | 201 | 400 Bad Request |
| `TestCreateTask_InvalidJSON` | 201 | 400 Bad Request |

`TestCreateTask_WithEnv` and `TestCreateTask_WithUsername` are unchanged (they already provide valid JSON bodies).

### 5. `router_test.go` — minimal changes

`TestCORS_*` tests call `router.ServeHTTP(rw, req)` which still works because Gin's engine implements `http.Handler`. The only required change is removing the now-deleted `corsMiddleware` standalone function if any test references it directly (none currently do).

### 6. `main.go` — optional cleanup

```go
// Before
http.ListenAndServe(":"+cfg.Server.Port, router)

// After — unchanged (Gin engine satisfies http.Handler), OR:
engine.Run(":" + cfg.Server.Port)  // only if NewRouter returns *gin.Engine instead of http.Handler
```

Keeping `NewRouter` returning `http.Handler` is recommended to avoid leaking Gin types into `main.go`.

---

## `types.go` change

Add `binding:"required"` to `sendMessageRequest.Prompt` so Gin's binding rejects empty or missing prompts:

```go
type sendMessageRequest struct {
    Prompt string `json:"prompt" binding:"required"`
}
```

`createTaskRequest` fields remain optional (no `binding` tags) — `ShouldBindJSON` will still return an error on malformed JSON, but valid JSON with absent fields is accepted.

---

## What does NOT change

- `types.go` — except `sendMessageRequest.Prompt` gains `binding:"required"` (see above)
- `TaskStore`, `SandboxManager`, `FileStore`, `MessageProxy` interfaces — unchanged
- `Handler` struct and `NewHandler` constructor — unchanged
- All business logic inside handler bodies — unchanged
- `sandbox/proxy.go` (`StreamMessage` implementation) — unchanged
- All other packages (`task`, `storage`, `sandbox`, `config`)

---

## Verification

After implementation, run the full test suite with race detection:

```bash
cd backend && go test -race ./...
```

Most existing tests pass without assertion changes. The two exceptions are `TestCreateTask_NoBody` and `TestCreateTask_InvalidJSON`, which must be updated to expect 400 instead of 201.

---

## Trade-offs / decisions

| Decision | Rationale |
|---|---|
| `gin.New()` over `gin.Default()` | Avoids Gin's default logger doubling log output with existing `log.Printf` calls |
| Keep `NewRouter` returning `http.Handler` | Avoids coupling `main.go` to Gin types; easier to swap routers in tests |
| Keep `MessageProxy` signature with `http.ResponseWriter` | `c.Writer` satisfies it; no interface churn downstream |
| `c.ShouldBindJSON` for all body decoding | Consistent Gin-idiomatic binding throughout; no manual `json.NewDecoder` |
| `binding:"required"` on `sendMessageRequest.Prompt` | Moves the empty-prompt validation into the struct tag, out of handler logic |
| No `gin.Logger()` middleware added | Existing handlers use `log.Printf`; adding Gin logger would duplicate output |
