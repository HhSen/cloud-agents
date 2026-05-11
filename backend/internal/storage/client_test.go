package storage_test

import (
	"context"
	"testing"

	"github.com/your-org/platform-backend/internal/storage"
	"github.com/your-org/platform-backend/pkg/config"
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
