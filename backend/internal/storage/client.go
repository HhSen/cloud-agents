package storage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type SessionMeta struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
	Version   string `json:"version"`
}

// OFSClient retrieves task history from OrangeFS via S3.
// History parts: {username}/history/{encoded_cwd}/{session_id}/part-*.ndjson
// Process records: {username}/.claude/sessions/{pid}.json
// Neither path is under the FUSE workspace mount; both are written by the agent server
// via the S3 API and read by the backend directly over the same endpoint.
type OFSClient interface {
	ListHistory(ctx context.Context, username, taskID string) ([]string, error)
	// GetHistory returns raw NDJSON entries from the session part files. Each
	// entry is the complete JSON object as stored on disk; no fields are dropped.
	// Entries with isMeta:true are excluded.
	GetHistory(ctx context.Context, key string) ([]json.RawMessage, error)
	GetSessionMeta(ctx context.Context, username, taskID string) (*SessionMeta, error)
	DeleteHistory(ctx context.Context, username, taskID string) error
}

// WorkspaceEntry is a single file or directory entry returned by ListWorkspace.
type WorkspaceEntry struct {
	Path    string    // absolute CWD-style path e.g. /workspace/alice/task-abc/src
	Name    string    // last path segment
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// Client is a concrete OFSClient backed by an S3-compatible endpoint.
type Client struct {
	s3     *s3.Client
	volume string
}

// New creates a new OFS S3 client using the given public endpoint URL.
// endpoint must include the scheme, e.g. "https://s3-yspu.didistatic.com".
// OFS ignores the AWS region; "us-east-1" is supplied only to satisfy the SDK.
func New(endpoint, volume, accessKey, secretKey string) (*Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			accessKey, secretKey, "",
		)),
		awsconfig.WithRegion("us-east-1"),
	)
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}

	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
		// OrangeFS does not support aws-chunked transfer encoding
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})

	return &Client{s3: s3c, volume: volume}, nil
}

// PutObject writes data to the given S3 key in the OFS volume.
func (c *Client) PutObject(ctx context.Context, key string, data []byte) error {
	size := int64(len(data))
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.volume),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: &size,
	})
	if err != nil {
		return fmt.Errorf("putting object %q: %w", key, err)
	}
	return nil
}

// GetObjectBytes downloads a single S3 object and returns its contents.
func (c *Client) GetObjectBytes(ctx context.Context, key string) ([]byte, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.volume),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("getting object %q: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("reading object %q: %w", key, err)
	}
	return data, nil
}

// Ping verifies connectivity by listing at most one object in the bucket.
func (c *Client) Ping(ctx context.Context) error {
	maxKeys := int32(1)
	_, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(c.volume),
		MaxKeys: &maxKeys,
	})
	return err
}

// encodeCWD converts an absolute workspace path to the Claude Code project-directory
// name used in S3. Claude Code derives these names by replacing every '/' with '-'.
// e.g. "/workspace/alice/task-id" → "-workspace-alice-task-id"
func encodeCWD(path string) string {
	return strings.ReplaceAll(path, "/", "-")
}

// ListHistory returns session prefixes for the given task.
// History files are stored under "{username}/history/{encoded_cwd}/{session_id}/".
// Each session directory contains one or more part-{timestamp}-{random}.ndjson files.
// The returned strings are S3 key prefixes of the form
// "{username}/history/{encoded_cwd}/{session_id}/".
func (c *Client) ListHistory(ctx context.Context, username, taskID string) ([]string, error) {
	cwd := fmt.Sprintf("/workspace/%s/%s", username, taskID)
	prefix := fmt.Sprintf("%s/history/%s/", username, encodeCWD(cwd))

	seen := make(map[string]bool)
	var sessions []string

	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.volume),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing objects under %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			rel := strings.TrimPrefix(key, prefix)
			// rel is "{session_id}/part-{timestamp}-{random}.ndjson"
			slash := strings.Index(rel, "/")
			if slash < 0 {
				continue
			}
			sessionPrefix := prefix + rel[:slash+1]
			if !seen[sessionPrefix] {
				seen[sessionPrefix] = true
				sessions = append(sessions, sessionPrefix)
			}
		}
	}
	return sessions, nil
}

// GetHistory downloads and parses all part files under a session prefix.
// Part files are sorted lexicographically (part-{timestamp}-* filenames make this
// equivalent to chronological order). Malformed lines and meta entries are excluded.
func (c *Client) GetHistory(ctx context.Context, sessionPrefix string) ([]json.RawMessage, error) {
	var partKeys []string

	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.volume),
		Prefix: aws.String(sessionPrefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing parts under %q: %w", sessionPrefix, err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if strings.HasSuffix(key, ".ndjson") || strings.HasSuffix(key, ".jsonl") {
				partKeys = append(partKeys, key)
			}
		}
	}
	sort.Strings(partKeys)

	var entries []json.RawMessage
	for _, key := range partKeys {
		part, err := c.readPart(ctx, key)
		if err != nil {
			return nil, err
		}
		entries = append(entries, part...)
	}
	return entries, nil
}

// readPart downloads a single .ndjson part file and returns each line as a raw
// JSON message. Only the isMeta field is decoded to filter internal entries;
// all other fields are preserved verbatim.
func (c *Client) readPart(ctx context.Context, key string) ([]json.RawMessage, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.volume),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("getting object %q: %w", key, err)
	}
	defer out.Body.Close()

	var entries []json.RawMessage
	scanner := bufio.NewScanner(out.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var filter struct {
			IsMeta bool `json:"isMeta"`
		}
		if err := json.Unmarshal(line, &filter); err != nil {
			continue
		}
		if filter.IsMeta {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)
		entries = append(entries, raw)
	}
	return entries, scanner.Err()
}

// DeleteHistory removes all session history and metadata files for a task from OFS.
// It deletes every object under "{username}/history/{encoded_cwd}/" and any
// session files under "{username}/.claude/sessions/" whose CWD matches the task workspace.
// Errors from the session-metadata scan are ignored (best-effort).
func (c *Client) DeleteHistory(ctx context.Context, username, taskID string) error {
	cwd := fmt.Sprintf("/workspace/%s/%s", username, taskID)
	historyPrefix := fmt.Sprintf("%s/history/%s/", username, encodeCWD(cwd))

	var keys []string

	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.volume),
		Prefix: aws.String(historyPrefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("listing history objects under %q: %w", historyPrefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}

	// best-effort: find session metadata files whose CWD matches this task
	sessionPrefix := fmt.Sprintf("%s/.claude/sessions/", username)
	sessionPaginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.volume),
		Prefix: aws.String(sessionPrefix),
	})
	for sessionPaginator.HasMorePages() {
		page, err := sessionPaginator.NextPage(ctx)
		if err != nil {
			break
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			data, err := c.GetObjectBytes(ctx, key)
			if err != nil {
				continue
			}
			var meta SessionMeta
			if err := json.Unmarshal(data, &meta); err != nil {
				continue
			}
			if meta.CWD == cwd {
				keys = append(keys, key)
			}
		}
	}

	// OFS requires Content-MD5 on DeleteObjects which the SDK omits, so delete one at a time.
	for _, key := range keys {
		if _, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(c.volume),
			Key:    aws.String(key),
		}); err != nil {
			return fmt.Errorf("deleting object %q: %w", key, err)
		}
	}
	return nil
}

// ListWorkspace returns one level of entries under subpath inside the task workspace.
// subpath is an absolute path like "/workspace/{username}/{task_id}/src"; the workspace
// root is "/workspace/{username}/{task_id}".
func (c *Client) ListWorkspace(ctx context.Context, username, taskID, subpath string) ([]WorkspaceEntry, error) {
	cwdBase := fmt.Sprintf("/workspace/%s/%s", username, taskID)
	relPath := strings.TrimPrefix(strings.TrimPrefix(subpath, cwdBase), "/")

	s3Base := fmt.Sprintf("%s/workspaces/%s/", username, taskID)
	listPrefix := s3Base
	if relPath != "" {
		listPrefix = fmt.Sprintf("%s/workspaces/%s/%s/", username, taskID, relPath)
	}

	var entries []WorkspaceEntry

	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket:    aws.String(c.volume),
		Prefix:    aws.String(listPrefix),
		Delimiter: aws.String("/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing workspace under %q: %w", listPrefix, err)
		}
		for _, cp := range page.CommonPrefixes {
			dirKey := aws.ToString(cp.Prefix)
			rel := strings.TrimSuffix(strings.TrimPrefix(dirKey, s3Base), "/")
			absPath := fmt.Sprintf("/workspace/%s/%s/%s", username, taskID, rel)
			entries = append(entries, WorkspaceEntry{
				Path:  absPath,
				Name:  path.Base(absPath),
				IsDir: true,
			})
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			rel := strings.TrimPrefix(key, s3Base)
			if rel == "" {
				continue
			}
			absPath := fmt.Sprintf("/workspace/%s/%s/%s", username, taskID, rel)
			var modTime time.Time
			if obj.LastModified != nil {
				modTime = *obj.LastModified
			}
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			entries = append(entries, WorkspaceEntry{
				Path:    absPath,
				Name:    path.Base(absPath),
				IsDir:   false,
				Size:    size,
				ModTime: modTime,
			})
		}
	}
	return entries, nil
}

// GetWorkspaceFile returns the raw bytes of a workspace file.
// filePath is an absolute path like "/workspace/{username}/{task_id}/src/main.py".
func (c *Client) GetWorkspaceFile(ctx context.Context, username, taskID, filePath string) ([]byte, error) {
	cwdBase := fmt.Sprintf("/workspace/%s/%s", username, taskID)
	relPath := strings.TrimPrefix(strings.TrimPrefix(filePath, cwdBase), "/")
	key := fmt.Sprintf("%s/workspaces/%s/%s", username, taskID, relPath)
	return c.GetObjectBytes(ctx, key)
}

// GetSessionMeta returns the Claude Code process record for the given task.
// It lists objects under "{username}/.claude/sessions/" and returns the first record
// whose CWD matches the expected workspace path "/workspace/{username}/{taskID}".
func (c *Client) GetSessionMeta(ctx context.Context, username, taskID string) (*SessionMeta, error) {
	prefix := fmt.Sprintf("%s/.claude/sessions/", username)
	expectedCWD := fmt.Sprintf("/workspace/%s/%s", username, taskID)

	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.volume),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing session objects: %w", err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(c.volume),
				Key:    aws.String(key),
			})
			if err != nil {
				continue
			}
			var meta SessionMeta
			decodeErr := json.NewDecoder(out.Body).Decode(&meta)
			out.Body.Close()
			if decodeErr != nil {
				continue
			}
			if meta.SessionID != "" && meta.CWD == expectedCWD {
				return &meta, nil
			}
		}
	}
	return nil, nil
}
