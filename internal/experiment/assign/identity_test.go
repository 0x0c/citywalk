package assign_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/experiment/assign"
)

func TestIdentityPrefersUserID(t *testing.T) {
	if got := assign.Identity("user-1", "channel-1"); got != "user-1" {
		t.Errorf("Identity = %q, want %q", got, "user-1")
	}
}

func TestIdentityFallsBackToChannelID(t *testing.T) {
	if got := assign.Identity("", "channel-1"); got != "channel-1" {
		t.Errorf("Identity = %q, want %q", got, "channel-1")
	}
}
