// Package assign implements CW-0008: experiment and holdout assignment by computation rather than
// by storage. Nothing here writes anything — the same identity and the same experiment yield the
// same bucket on any server, on any device, at any time, including after a database restore.
package assign

// Identity returns the identity CW-0008 Unit 1 says assignment hashes: the user identifier when the
// channel is associated with an account, and the channel identifier otherwise. Preferring the user
// identifier is what keeps an experiment consistent across a user's phone and tablet, a property a
// per-device identity cannot offer.
//
// The device-side half of Unit 1 — pinning the variant to the impression once displayed, so a login
// mid-experiment does not flip an already-shown message to a different arm — is client SDK behavior
// and is not implemented here; docs/requirements.md rules the SDK permanently out of scope for this
// repository.
func Identity(userID, channelID string) string {
	if userID != "" {
		return userID
	}
	return channelID
}
