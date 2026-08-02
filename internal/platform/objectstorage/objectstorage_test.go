package objectstorage_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/0x0c/citywalk/internal/platform/objectstorage"
)

func TestHashIsSHA256Hex(t *testing.T) {
	data := []byte("citywalk campaign asset")
	want := sha256.Sum256(data)

	got := objectstorage.Hash(data)
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("Hash(%q) = %q, want the SHA-256 hex digest %q", data, got, hex.EncodeToString(want[:]))
	}
}

func TestHashIsDeterministicAndContentAddressed(t *testing.T) {
	a := objectstorage.Hash([]byte("same bytes"))
	b := objectstorage.Hash([]byte("same bytes"))
	if a != b {
		t.Errorf("Hash of identical bytes differed: %q vs %q", a, b)
	}

	c := objectstorage.Hash([]byte("different bytes"))
	if a == c {
		t.Error("Hash of different bytes collided")
	}
}

func TestKeyUsesTheSHA256Namespace(t *testing.T) {
	hash := objectstorage.Hash([]byte("payload"))
	key := objectstorage.Key(hash)

	if !strings.HasPrefix(key, "assets/sha256/") {
		t.Errorf("Key(%q) = %q, want prefix %q", hash, key, "assets/sha256/")
	}
	if !strings.HasSuffix(key, hash) {
		t.Errorf("Key(%q) = %q, want it to end in the hash", hash, key)
	}
}

func TestAssetURLJoinsBaseAndKey(t *testing.T) {
	got := objectstorage.AssetURL("https://cdn.citywalk.example", "assets/sha256/abc123")
	want := "https://cdn.citywalk.example/assets/sha256/abc123"
	if got != want {
		t.Errorf("AssetURL = %q, want %q", got, want)
	}
}

func TestAssetURLTrimsATrailingSlashOnBase(t *testing.T) {
	got := objectstorage.AssetURL("https://cdn.citywalk.example/", "assets/sha256/abc123")
	want := "https://cdn.citywalk.example/assets/sha256/abc123"
	if got != want {
		t.Errorf("AssetURL = %q, want %q", got, want)
	}
}

func TestIsContentAddressedURLAcceptsAWellFormedAssetURL(t *testing.T) {
	hash := objectstorage.Hash([]byte("some image bytes"))
	url := objectstorage.AssetURL("https://cdn.citywalk.example", objectstorage.Key(hash))

	if !objectstorage.IsContentAddressedURL(url) {
		t.Errorf("IsContentAddressedURL(%q) = false, want true for a URL Upload could have produced", url)
	}
}

func TestIsContentAddressedURLAcceptsAnyHostOrPathPrefix(t *testing.T) {
	// The delivery-network origin is deployment-specific configuration; the check must not pin one
	// particular host, and a CDN is free to serve the bucket from a sub-path.
	hash := objectstorage.Hash([]byte("some image bytes"))
	url := "https://media.other-cdn.example/region/us/" + objectstorage.Key(hash)

	if !objectstorage.IsContentAddressedURL(url) {
		t.Errorf("IsContentAddressedURL(%q) = false, want true regardless of host or path prefix", url)
	}
}

func TestIsContentAddressedURLRejectsAnArbitraryString(t *testing.T) {
	for _, url := range []string{
		"",
		"not-a-url-at-all",
		"https://cdn.citywalk.example/whatever-the-author-typed.png",
		"https://cdn.citywalk.example/assets/sha256/too-short",
		"https://cdn.citywalk.example/assets/md5/" + strings.Repeat("a", 32),
	} {
		if objectstorage.IsContentAddressedURL(url) {
			t.Errorf("IsContentAddressedURL(%q) = true, want false for a non-content-addressed reference", url)
		}
	}
}

func TestIsContentAddressedURLRejectsAnUppercaseHash(t *testing.T) {
	// Hash always emits lowercase hex; an uppercase-hex URL is not a value this package's own Upload
	// could have produced, whatever store it might otherwise resolve against.
	url := "https://cdn.citywalk.example/assets/sha256/" + strings.ToUpper(objectstorage.Hash([]byte("x")))
	if objectstorage.IsContentAddressedURL(url) {
		t.Errorf("IsContentAddressedURL(%q) = true, want false for an uppercase-hex key", url)
	}
}

func TestIsContentAddressedURLRejectsNonHTTPSSchemes(t *testing.T) {
	hash := objectstorage.Hash([]byte("some image bytes"))
	key := objectstorage.Key(hash)
	for _, url := range []string{
		"http://cdn.citywalk.example/" + key,
		"javascript:alert(1)//" + key,
		"data:text/html;base64," + key,
	} {
		if objectstorage.IsContentAddressedURL(url) {
			t.Errorf("IsContentAddressedURL(%q) = true, want false for a disallowed scheme", url)
		}
	}
}

func TestUploadURLRoundTripsThroughIsContentAddressedURL(t *testing.T) {
	hash := objectstorage.Hash([]byte("round trip"))
	url := objectstorage.AssetURL("https://cdn.citywalk.example", objectstorage.Key(hash))

	if !objectstorage.IsContentAddressedURL(url) {
		t.Errorf("a URL built from Hash and Key via AssetURL must satisfy IsContentAddressedURL, got false for %q", url)
	}
}
