package validate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/validate"
)

func validMessage(now time.Time) model.Message {
	return model.Message{
		ID:     "message-1",
		Name:   "Route complete",
		State:  model.MessageStateDraft,
		Window: model.Window{Start: now, End: now.Add(24 * time.Hour)},
		Variants: []model.Variant{
			{
				ID:            "variant-1",
				Weight:        100,
				Language:      "en",
				SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor, Minor: 0},
				Content: model.DialogContent{
					Presentation: model.Presentation{
						Heading: "Nice walk",
						Buttons: []model.Button{
							{Label: "Learn more", Actions: []model.Action{
								model.OpenLinkAction{URL: "https://citywalk.example/routes"},
							}},
						},
					},
				},
			},
		},
	}
}

func TestValidateAcceptsAWellFormedMessage(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if err := validate.Validate(context.Background(), nil, validMessage(now), now); err != nil {
		t.Fatalf("Validate: %v, want nil", err)
	}
}

func TestValidateRejectsWindowEndBeforeStart(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Window = model.Window{Start: now.Add(2 * time.Hour), End: now.Add(time.Hour)}

	err := validate.Validate(context.Background(), nil, msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a temporal sanity error")
	}
	if !strings.Contains(err.Error(), "must precede") {
		t.Errorf("Validate error = %q, want it to mention the window ordering", err)
	}
}

func TestValidateRejectsWindowEndInThePast(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Window = model.Window{Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)}

	err := validate.Validate(context.Background(), nil, msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a temporal sanity error")
	}
	if !strings.Contains(err.Error(), "must be in the future") {
		t.Errorf("Validate error = %q, want it to mention the window end is in the past", err)
	}
}

func TestValidateRejectsVariantWeightsNotSummingTo100(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Variants[0].Weight = 60

	err := validate.Validate(context.Background(), nil, msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a variant weight error")
	}
	if !strings.Contains(err.Error(), "sum to 60") {
		t.Errorf("Validate error = %q, want it to report the actual sum", err)
	}
}

func TestValidateAcceptsIndependentWeightTotalsPerLanguage(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Variants = []model.Variant{
		{Weight: 70, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{}},
		{Weight: 30, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{}},
		{Weight: 100, Language: "ja", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{}},
	}

	if err := validate.Validate(context.Background(), nil, msg, now); err != nil {
		t.Fatalf("Validate: %v, want nil", err)
	}
}

func TestValidateRejectsUnsupportedSchemaMajor(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Variants[0].SchemaVersion = model.SchemaVersion{Major: model.CurrentMajor + 1}

	err := validate.Validate(context.Background(), nil, msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a schema version error")
	}
	if !strings.Contains(err.Error(), "schema major") {
		t.Errorf("Validate error = %q, want it to mention the unsupported major", err)
	}
}

// withSupportedMajors temporarily widens model.SupportedMajors to majors for the duration of the
// test, restoring the original value on cleanup — simulating the mid-migration state CW-0003 Unit 4
// describes, where a second Major is admitted alongside model.CurrentMajor for the overlap period.
func withSupportedMajors(t *testing.T, majors ...int) {
	t.Helper()
	original := model.SupportedMajors
	model.SupportedMajors = majors
	t.Cleanup(func() { model.SupportedMajors = original })
}

// TestValidateAcceptsANewMajorAlongsideCurrentDuringATransition demonstrates CW-0003 Unit 4's
// parallel emission actually has something to emit: once a migration to a new Major is under way
// (model.SupportedMajors widened to name it), a message can save one variant declaring
// model.CurrentMajor and another declaring the new Major side by side — the gap the Progress note
// previously recorded (save-time validation admitted only model.CurrentMajor) is closed.
func TestValidateAcceptsANewMajorAlongsideCurrentDuringATransition(t *testing.T) {
	newMajor := model.CurrentMajor + 1
	withSupportedMajors(t, model.CurrentMajor, newMajor)

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Variants = append(msg.Variants, model.Variant{
		ID:            "variant-2",
		Weight:        100,
		Language:      "ja",
		SchemaVersion: model.SchemaVersion{Major: newMajor},
		Content: model.DialogContent{
			Presentation: model.Presentation{Heading: "散歩完了"},
		},
	})

	if err := validate.Validate(context.Background(), nil, msg, now); err != nil {
		t.Fatalf("Validate: %v, want nil for a Major the transition has widened SupportedMajors to admit", err)
	}
}

// TestValidateStillRejectsAMajorOutsideTheTransition demonstrates the widened SupportedMajors set is
// still a short, explicit list rather than an open range: a Major named by neither entry is refused
// exactly as before the transition began.
func TestValidateStillRejectsAMajorOutsideTheTransition(t *testing.T) {
	withSupportedMajors(t, model.CurrentMajor, model.CurrentMajor+1)

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Variants[0].SchemaVersion = model.SchemaVersion{Major: model.CurrentMajor + 2}

	err := validate.Validate(context.Background(), nil, msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a schema version error for a Major outside the transition's pair")
	}
	if !strings.Contains(err.Error(), "schema major") {
		t.Errorf("Validate error = %q, want it to mention the unsupported major", err)
	}
}

// TestValidateStillRejectsAnUnknownLayoutDuringATransition demonstrates additive-only-within-a-minor
// is unaffected by widening SupportedMajors: even mid-transition, a variant's Content still has to
// decode through model.UnmarshalContent, which rejects a layout this build does not know rather than
// passing it through — CW-0003 Unit 4's "skip rather than guess" rule holds regardless of which
// Majors save-time validation currently admits.
func TestValidateStillRejectsAnUnknownLayoutDuringATransition(t *testing.T) {
	withSupportedMajors(t, model.CurrentMajor, model.CurrentMajor+1)

	_, err := model.UnmarshalContent([]byte(`{"layout":"holographic_projection"}`))
	if err == nil {
		t.Fatal("UnmarshalContent: got nil error for an unknown layout during a transition, want an error")
	}
}

func TestValidateRejectsDisallowedLinkScheme(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Variants[0].Content = model.DialogContent{
		Presentation: model.Presentation{
			Buttons: []model.Button{
				{Label: "Open", Actions: []model.Action{
					model.OpenLinkAction{URL: "javascript:alert(1)"},
				}},
			},
		},
	}

	err := validate.Validate(context.Background(), nil, msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a content security error")
	}
	if !strings.Contains(err.Error(), "disallowed scheme") {
		t.Errorf("Validate error = %q, want it to mention the disallowed scheme", err)
	}
}

func TestValidateRejectsExecutableSchemeInHTMLContent(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Variants[0].Content = model.HTMLContent{HTML: `<a href="JavaScript:alert(1)">click</a>`}

	err := validate.Validate(context.Background(), nil, msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a content security error")
	}
	if !strings.Contains(err.Error(), "forbidden scheme") {
		t.Errorf("Validate error = %q, want it to mention the forbidden scheme", err)
	}
}

func TestValidateChecksButtonsInsideSequenceSteps(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Variants[0].Content = model.SequenceContent{
		Steps: []model.Content{
			model.DialogContent{
				Presentation: model.Presentation{
					Buttons: []model.Button{
						{Label: "Bad", Actions: []model.Action{
							model.OpenLinkAction{URL: "javascript:alert(1)"},
						}},
					},
				},
			},
		},
	}

	err := validate.Validate(context.Background(), nil, msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a content security error from the sequence step")
	}
}
