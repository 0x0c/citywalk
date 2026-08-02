// Package objectstorage wires CW-0010 Unit 7's creative-asset store: an S3-compatible object storage
// client, content-addressed keys, and the delivery-network URL a MediaRef stores. Per Unit 11's
// staged-adoption text, this package ships and is tested in phase one's codebase even though nothing
// in phase one's default configuration calls New — it becomes load-bearing the day a caller uploads
// through it, not the day this code merges.
package objectstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// keyPrefix and hashHexLength together define the content-addressed key scheme Upload writes under
// and IsContentAddressedURL/keyFromURL recognize: "assets/sha256/<64 lowercase hex characters>". A
// fixed, narrow shape (rather than an open "anything under assets/") is what lets validate reject an
// arbitrary string instead of merely an arbitrary string with the right prefix.
const (
	keyPrefix     = "assets/sha256/"
	hashHexLength = sha256.Size * 2 // 64
)

// assetPathPattern matches the path component of a URL Upload could have produced, regardless of
// host or of any path prefix the delivery network's base URL itself contributes (a CDN is free to
// serve the bucket from a sub-path). Anchored at the end of the path, not the start, for that reason.
var assetPathPattern = regexp.MustCompile(`/` + keyPrefix + `([0-9a-f]{` + fmt.Sprint(hashHexLength) + `})$`)

// Hash returns the hex-encoded SHA-256 digest of data — the same construction
// internal/audience/predicate.Compile already uses to key its own compilation cache, reused here
// rather than reinvented. SHA-256 rather than the etag package's FNV-64a: that hash is deliberately
// non-cryptographic (internal/delivery/etag's own doc comment: it guards staleness between a server
// and a device that already trust each other). A content-addressed asset key is a different property
// — two different byte strings must not plausibly collide onto the same URL — so it needs the
// cryptographic hash the etag package explicitly opted out of.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Key returns the object storage key an asset with the given content hash is stored under. hash must
// be a value Hash produced; Key does not itself validate its shape.
func Key(hash string) string {
	return keyPrefix + hash
}

// AssetURL joins baseURL (a delivery-network origin, e.g. "https://cdn.citywalk.example") and key
// into the URL a MediaRef stores.
func AssetURL(baseURL, key string) string {
	return strings.TrimRight(baseURL, "/") + "/" + key
}

// keyFromURL extracts the content-addressed key from rawURL if rawURL has the shape Upload produces:
// HTTPS, a host, and a path ending in the keyPrefix/hash pattern. It reports ok=false for anything
// else, including a syntactically invalid URL, a non-HTTPS scheme, or a well-formed HTTPS URL whose
// path simply isn't a content-addressed asset reference.
func keyFromURL(rawURL string) (key string, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	match := assetPathPattern.FindString(u.Path)
	if match == "" {
		return "", false
	}
	return strings.TrimPrefix(match, "/"), true
}

// IsContentAddressedURL reports whether rawURL has the shape Upload produces: an HTTPS URL whose path
// ends in "assets/sha256/<64 lowercase hex characters>". This is CW-0003 Unit 5's media
// referential-integrity check — see internal/definition/validate's doc comment for why it stops at
// shape and does not confirm the object exists in the store.
func IsContentAddressedURL(rawURL string) bool {
	_, ok := keyFromURL(rawURL)
	return ok
}

// Client is a connected object storage client plus the delivery-network origin Upload's returned
// URLs are built against.
type Client struct {
	minio         *minio.Client
	bucket        string
	publicBaseURL string
}

// Config is the deployment-specific configuration New needs. Every field varies by environment, so
// (per .agent-workflows/implement/workflow.md's no-secret-material guardrail) none of it is
// hard-coded — the caller reads it from configuration the way internal/platform/config.Load reads
// Postgres and Redis settings today.
type Config struct {
	// Endpoint is the object storage host:port, without a scheme (the minio-go convention).
	Endpoint string
	// AccessKeyID and SecretAccessKey authenticate against Endpoint.
	AccessKeyID     string
	SecretAccessKey string
	// UseSSL selects HTTPS for the connection to Endpoint. This is independent of the HTTPS
	// IsContentAddressedURL requires of PublicBaseURL: Endpoint is the object storage API a server
	// process talks to (may be a private, self-hosted MinIO on plain HTTP inside a private network),
	// PublicBaseURL is the delivery network devices fetch assets from and must be HTTPS regardless.
	UseSSL bool
	// Bucket is the bucket Upload writes to and Exists reads from. New fails if it does not exist —
	// creating a bucket is an operational action taken once per environment, not a side effect this
	// client performs on a caller's behalf.
	Bucket string
	// PublicBaseURL is the delivery-network origin AssetURL joins a key onto.
	PublicBaseURL string
}

// New connects to cfg.Endpoint and verifies cfg.Bucket exists before returning, the same
// reachability-before-return contract internal/platform/redisclient.New already applies. Nothing in
// this codebase's default configuration calls New (CW-0010 Unit 11's config-gated staging): a process
// that never sets up an object storage configuration never calls New and so never assumes a bucket is
// reachable at startup.
func New(ctx context.Context, cfg Config) (*Client, error) {
	minioClient, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("objectstorage: build client: %w", err)
	}

	exists, err := minioClient.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("objectstorage: check bucket %q: %w", cfg.Bucket, err)
	}
	if !exists {
		return nil, fmt.Errorf("objectstorage: bucket %q does not exist", cfg.Bucket)
	}

	return &Client{minio: minioClient, bucket: cfg.Bucket, publicBaseURL: cfg.PublicBaseURL}, nil
}

// Upload writes data under the key its content hash derives and returns the asset's URL. Re-uploading
// identical bytes is idempotent and returns the same URL every time (CW-0010 Unit 7's whole point: an
// asset's URL never needs to change unless the bytes do), and costs only a StatObject rather than a
// redundant PutObject, since the destination key already names bytes identical to data by
// construction — there is nothing to overwrite.
func (c *Client) Upload(ctx context.Context, data []byte, contentType string) (string, error) {
	hash := Hash(data)
	key := Key(hash)

	_, err := c.minio.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	switch {
	case err == nil:
		return AssetURL(c.publicBaseURL, key), nil
	case minio.ToErrorResponse(err).Code != "NoSuchKey":
		return "", fmt.Errorf("objectstorage: stat %s: %w", key, err)
	}

	if _, err := c.minio.PutObject(ctx, c.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType},
	); err != nil {
		return "", fmt.Errorf("objectstorage: put %s: %w", key, err)
	}
	return AssetURL(c.publicBaseURL, key), nil
}

// Exists reports whether the asset rawURL names is actually present in the store — the real,
// round-trip existence check CW-0003 Unit 5's save-time validation deliberately does not call (see
// internal/definition/validate's doc comment). It is exported for a caller that does need the
// stronger guarantee outside the save path: an asset-management audit job, for instance, that can
// afford the round trip because it is not gating every definition save on the store's reachability.
func (c *Client) Exists(ctx context.Context, rawURL string) (bool, error) {
	key, ok := keyFromURL(rawURL)
	if !ok {
		return false, fmt.Errorf("objectstorage: %q is not a content-addressed asset URL", rawURL)
	}
	_, err := c.minio.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return false, nil
	}
	return false, fmt.Errorf("objectstorage: stat %s: %w", key, err)
}
