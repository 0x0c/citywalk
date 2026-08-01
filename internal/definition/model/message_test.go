package model_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/definition/model"
)

func TestCanTransitionAllowsTheNoOpCase(t *testing.T) {
	for _, s := range []model.MessageState{
		model.MessageStateDraft, model.MessageStateScheduled, model.MessageStateActive,
		model.MessageStatePaused, model.MessageStateCompleted, model.MessageStateArchived,
	} {
		if !s.CanTransition(s) {
			t.Errorf("CanTransition(%q, %q) = false, want true (a state always permits itself)", s, s)
		}
	}
}

func TestCanTransitionAllowsEveryDocumentedForwardMove(t *testing.T) {
	cases := []struct{ from, to model.MessageState }{
		{model.MessageStateDraft, model.MessageStateScheduled},
		{model.MessageStateDraft, model.MessageStateActive},
		{model.MessageStateDraft, model.MessageStateArchived},
		{model.MessageStateScheduled, model.MessageStateActive},
		{model.MessageStateScheduled, model.MessageStatePaused},
		{model.MessageStateScheduled, model.MessageStateArchived},
		{model.MessageStateActive, model.MessageStatePaused},
		{model.MessageStateActive, model.MessageStateCompleted},
		{model.MessageStateActive, model.MessageStateArchived},
		{model.MessageStatePaused, model.MessageStateActive},
		{model.MessageStatePaused, model.MessageStateCompleted},
		{model.MessageStatePaused, model.MessageStateArchived},
		{model.MessageStateCompleted, model.MessageStateArchived},
	}
	for _, tc := range cases {
		if !tc.from.CanTransition(tc.to) {
			t.Errorf("CanTransition(%q -> %q) = false, want true", tc.from, tc.to)
		}
	}
}

func TestCanTransitionRejectsGoingBackward(t *testing.T) {
	cases := []struct{ from, to model.MessageState }{
		{model.MessageStateActive, model.MessageStateDraft},
		{model.MessageStateActive, model.MessageStateScheduled},
		{model.MessageStatePaused, model.MessageStateDraft},
		{model.MessageStateCompleted, model.MessageStateActive},
		{model.MessageStateCompleted, model.MessageStatePaused},
		{model.MessageStateArchived, model.MessageStateActive},
		{model.MessageStateArchived, model.MessageStateDraft},
	}
	for _, tc := range cases {
		if tc.from.CanTransition(tc.to) {
			t.Errorf("CanTransition(%q -> %q) = true, want false (a transition only moves forward)", tc.from, tc.to)
		}
	}
}

func TestCanTransitionRejectsSkippingIntoScheduledOrDraftFromActive(t *testing.T) {
	// draft can reach active directly (skipping scheduled is allowed forward), but nothing may ever
	// move back into draft once it has left it.
	if !model.MessageStateDraft.CanTransition(model.MessageStateActive) {
		t.Error("CanTransition(draft -> active) = false, want true")
	}
	for _, from := range []model.MessageState{
		model.MessageStateScheduled, model.MessageStateActive, model.MessageStatePaused,
		model.MessageStateCompleted, model.MessageStateArchived,
	} {
		if from.CanTransition(model.MessageStateDraft) {
			t.Errorf("CanTransition(%q -> draft) = true, want false", from)
		}
	}
}

func TestCanTransitionRejectsAnythingOutOfArchived(t *testing.T) {
	for _, to := range []model.MessageState{
		model.MessageStateDraft, model.MessageStateScheduled, model.MessageStateActive,
		model.MessageStatePaused, model.MessageStateCompleted,
	} {
		if model.MessageStateArchived.CanTransition(to) {
			t.Errorf("CanTransition(archived -> %q) = true, want false (archived is terminal)", to)
		}
	}
}
