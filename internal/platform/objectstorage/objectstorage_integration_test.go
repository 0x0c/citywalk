//go:build integration

// Run with: go test -tags=integration ./internal/platform/objectstorage/... with
// CITYWALK_TEST_S3_ENDPOINT, CITYWALK_TEST_S3_BUCKET, CITYWALK_TEST_S3_ACCESS_KEY, and
// CITYWALK_TEST_S3_SECRET_KEY pointing at a scratch S3-compatible bucket (e.g. a local MinIO). No such
// endpoint is reachable in the sandbox this pass was implemented in (ports 9000/9001 closed), so this
// suite is untested live and simply skips there and in ordinary CI, matching every other
// store-backed *_integration_test.go in this repository.
package objectstorage_test

import (
	"context"
	"os"
	"testing"

	"github.com/0x0c/citywalk/internal/platform/objectstorage"
)

func testConfig(t *testing.T) objectstorage.Config {
	t.Helper()
	endpoint := os.Getenv("CITYWALK_TEST_S3_ENDPOINT")
	bucket := os.Getenv("CITYWALK_TEST_S3_BUCKET")
	accessKey := os.Getenv("CITYWALK_TEST_S3_ACCESS_KEY")
	secretKey := os.Getenv("CITYWALK_TEST_S3_SECRET_KEY")
	if endpoint == "" || bucket == "" || accessKey == "" || secretKey == "" {
		t.Skip("CITYWALK_TEST_S3_ENDPOINT, CITYWALK_TEST_S3_BUCKET, CITYWALK_TEST_S3_ACCESS_KEY, and CITYWALK_TEST_S3_SECRET_KEY must all be set")
	}
	return objectstorage.Config{
		Endpoint:        endpoint,
		Bucket:          bucket,
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
		UseSSL:          false,
		PublicBaseURL:   "https://cdn.citywalk.example",
	}
}

func TestNewFailsAgainstAMissingBucket(t *testing.T) {
	cfg := testConfig(t)
	cfg.Bucket = cfg.Bucket + "-does-not-exist"

	if _, err := objectstorage.New(context.Background(), cfg); err == nil {
		t.Fatal("New: got nil error for a bucket that does not exist, want one")
	}
}

// TestUploadIsContentAddressedAndIdempotent is CW-0010 Unit 7's whole point end to end: uploading the
// same bytes twice returns the same URL, and the object actually lands in the store under it.
func TestUploadIsContentAddressedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	client, err := objectstorage.New(ctx, testConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	data := []byte("an image's worth of bytes, for this test's purposes")

	first, err := client.Upload(ctx, data, "application/octet-stream")
	if err != nil {
		t.Fatalf("Upload (first): %v", err)
	}
	second, err := client.Upload(ctx, data, "application/octet-stream")
	if err != nil {
		t.Fatalf("Upload (second): %v", err)
	}
	if first != second {
		t.Errorf("Upload of identical bytes returned different URLs: %q vs %q", first, second)
	}

	exists, err := client.Exists(ctx, first)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("Exists = false for a URL Upload just returned, want true")
	}

	if !objectstorage.IsContentAddressedURL(first) {
		t.Errorf("Upload returned %q, which IsContentAddressedURL does not recognize as its own shape", first)
	}
}

// TestUploadOfDifferentBytesProducesDifferentURLs demonstrates the other half of content addressing:
// an edited asset gets a new URL rather than overwriting the old one in place (CW-0010 Unit 7's
// "an edited image that reaches half the devices is a campaign displaying two different things").
func TestUploadOfDifferentBytesProducesDifferentURLs(t *testing.T) {
	ctx := context.Background()
	client, err := objectstorage.New(ctx, testConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	before, err := client.Upload(ctx, []byte("version one"), "application/octet-stream")
	if err != nil {
		t.Fatalf("Upload (before): %v", err)
	}
	after, err := client.Upload(ctx, []byte("version two"), "application/octet-stream")
	if err != nil {
		t.Fatalf("Upload (after): %v", err)
	}
	if before == after {
		t.Error("Upload of different bytes returned the same URL, want distinct URLs")
	}
}

func TestExistsIsFalseForAWellFormedURLNeverUploaded(t *testing.T) {
	ctx := context.Background()
	client, err := objectstorage.New(ctx, testConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hash := objectstorage.Hash([]byte("bytes nobody ever uploaded, this test included"))
	url := objectstorage.AssetURL("https://cdn.citywalk.example", objectstorage.Key(hash))

	exists, err := client.Exists(ctx, url)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("Exists = true for a URL that was never uploaded, want false")
	}
}

func TestExistsRejectsAURLThatIsNotContentAddressed(t *testing.T) {
	ctx := context.Background()
	client, err := objectstorage.New(ctx, testConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := client.Exists(ctx, "https://cdn.citywalk.example/whatever.png"); err == nil {
		t.Fatal("Exists: got nil error for a non-content-addressed URL, want one")
	}
}
