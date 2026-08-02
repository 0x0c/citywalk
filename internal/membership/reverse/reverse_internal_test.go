package reverse

import (
	"strings"
	"testing"
)

// TestKeyNamespacesEveryChannelUnderTheReversePrefix is the collision guard CW-0010 Unit 4's shared
// Redis instance needs: CW-0006's entity-tag cache and CW-0007's counters live alongside this index,
// so an unprefixed channel identifier would let one subsystem overwrite another's value.
func TestKeyNamespacesEveryChannelUnderTheReversePrefix(t *testing.T) {
	got := key("channel-1")
	if !strings.HasPrefix(got, keyPrefix) {
		t.Errorf("key(%q) = %q, want it prefixed with %q", "channel-1", got, keyPrefix)
	}
	if !strings.HasSuffix(got, "channel-1") {
		t.Errorf("key(%q) = %q, want the channel identifier preserved verbatim", "channel-1", got)
	}
}

func TestKeyIsDistinctPerChannel(t *testing.T) {
	if key("channel-1") == key("channel-2") {
		t.Errorf("key(%q) == key(%q) = %q, want distinct keys", "channel-1", "channel-2", key("channel-1"))
	}
}
