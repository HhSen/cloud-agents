package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

type ConversationEntry struct {
	Type       string          `json:"type"`
	UUID       string          `json:"uuid"`
	ParentUUID string          `json:"parentUuid"`
	Timestamp  string          `json:"timestamp"`
	IsMeta     bool            `json:"isMeta"`
	Message    json.RawMessage `json:"message"`
}

// OFSClient retrieves task history from OrangeFS via S3.
// History files are stored under the user's .claude directory, mirroring the
// local ~/.claude layout: {username}/.claude/projects/{encoded_cwd}/{session_id}.jsonl
// The OFS mount is set up at sandbox creation time, before any agent session exists.
type OFSClient interface {
	ListHistory(ctx context.Context, username, taskID string) ([]string, error)
	GetHistory(ctx context.Context, key string) ([]ConversationEntry, error)
	GetSessionMeta(ctx context.Context, username, taskID string) (*SessionMeta, error)
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
	})

	return &Client{s3: s3c, volume: volume}, nil
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

// ListHistory returns all Claude Code JSONL history keys for the given task.
// Keys are stored under "{username}/.claude/projects/{encoded_cwd}/" where
// encoded_cwd is the workspace path "/workspace/{username}/{taskID}" with slashes
// replaced by hyphens. Only direct .jsonl files are returned; subdirectory files
// (subagent transcripts) are excluded.
func (c *Client) ListHistory(ctx context.Context, username, taskID string) ([]string, error) {
	cwd := fmt.Sprintf("/workspace/%s/%s", username, taskID)
	prefix := fmt.Sprintf("%s/.claude/projects/%s/", username, encodeCWD(cwd))
	var keys []string

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
			if strings.HasSuffix(rel, ".jsonl") && !strings.Contains(rel, "/") {
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}

// GetHistory downloads and parses a Claude Code JSONL history file by its S3 key.
// Malformed lines are silently skipped; meta entries (IsMeta=true) are excluded.
func (c *Client) GetHistory(ctx context.Context, key string) ([]ConversationEntry, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.volume),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("getting object %q: %w", key, err)
	}
	defer out.Body.Close()

	var entries []ConversationEntry
	scanner := bufio.NewScanner(out.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry ConversationEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if !entry.IsMeta {
			entries = append(entries, entry)
		}
	}
	return entries, scanner.Err()
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
