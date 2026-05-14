package storage_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/l-lab/cloud-agents/internal/storage"
	"github.com/l-lab/cloud-agents/pkg/config"
)

// TestConnection verifies that the S3 client can reach the OFS endpoint.
// Run with:
//
//	go test ./internal/storage/ -v -run TestConnection
func TestConnection(t *testing.T) {
	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		t.Skipf("skipping: could not load config.yaml: %v", err)
	}

	ofs := cfg.OrangeFS
	if ofs.Endpoint == "" || ofs.AccessKey == "" || ofs.SecretKey == "" {
		t.Skip("skipping: orangefs endpoint/access_key/secret_key not configured")
	}

	client, err := storage.New(ofs.Endpoint, ofs.Volume, ofs.AccessKey, ofs.SecretKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	t.Logf("connection OK — endpoint=%s bucket=%s", ofs.Endpoint, ofs.Volume)
}

// ---- fake S3 helpers ----

// newFakeS3 returns an httptest.Server that stores PUT bodies and replays them on GET.
// It simulates path-style S3 requests: PUT/GET /{bucket}/{key}.
func newFakeS3(t *testing.T) (srv *httptest.Server, objects *sync.Map) {
	t.Helper()
	objects = &sync.Map{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			objects.Store(r.URL.Path, body)
			w.Header().Set("ETag", `"test-etag"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if v, ok := objects.Load(r.URL.Path); ok {
				data := v.([]byte)
				w.Header().Set("ETag", `"test-etag"`)
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				w.WriteHeader(http.StatusOK)
				w.Write(data) //nolint:errcheck
			} else {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`) //nolint:errcheck
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, objects
}

func newTestStorageClient(t *testing.T, srv *httptest.Server) *storage.Client {
	t.Helper()
	c, err := storage.New(srv.URL, "test-vol", "access", "secret")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	return c
}

// ---- PutObject ----

func TestPutObject_Success(t *testing.T) {
	srv, objects := newFakeS3(t)
	c := newTestStorageClient(t, srv)

	data := []byte("# My Skill\n")
	key := "alice/resources/skills/my-sk/SKILL.md"
	if err := c.PutObject(context.Background(), key, data); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	wantPath := "/test-vol/" + key
	v, ok := objects.Load(wantPath)
	if !ok {
		t.Fatalf("object not stored at path %q", wantPath)
	}
	if string(v.([]byte)) != string(data) {
		t.Errorf("stored body = %q, want %q", v, data)
	}
}

func TestPutObject_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InternalError</Code><Message>server error</Message></Error>`) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	c := newTestStorageClient(t, srv)

	if err := c.PutObject(context.Background(), "some/key", []byte("data")); err == nil {
		t.Error("expected error on 500, got nil")
	}
}

// ---- GetObjectBytes ----

func TestGetObjectBytes_Success(t *testing.T) {
	srv, _ := newFakeS3(t)
	c := newTestStorageClient(t, srv)

	want := []byte(`{"type":"stdio","command":"npx"}`)
	key := "alice/resources/mcp/gh.json"
	if err := c.PutObject(context.Background(), key, want); err != nil {
		t.Fatalf("PutObject setup: %v", err)
	}

	got, err := c.GetObjectBytes(context.Background(), key)
	if err != nil {
		t.Fatalf("GetObjectBytes: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGetObjectBytes_NotFound(t *testing.T) {
	srv, _ := newFakeS3(t)
	c := newTestStorageClient(t, srv)

	_, err := c.GetObjectBytes(context.Background(), "alice/missing/key.txt")
	if err == nil {
		t.Error("expected error for missing key, got nil")
	}
}

func TestGetObjectBytes_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InternalError</Code><Message>server error</Message></Error>`) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	c := newTestStorageClient(t, srv)

	_, err := c.GetObjectBytes(context.Background(), "some/key")
	if err == nil {
		t.Error("expected error on 500, got nil")
	}
}
