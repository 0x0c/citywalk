package validate_test

import (
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
	if err := validate.Validate(validMessage(now), now); err != nil {
		t.Fatalf("Validate: %v, want nil", err)
	}
}

func TestValidateRejectsWindowEndBeforeStart(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Window = model.Window{Start: now.Add(2 * time.Hour), End: now.Add(time.Hour)}

	err := validate.Validate(msg, now)
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

	err := validate.Validate(msg, now)
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

	err := validate.Validate(msg, now)
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

	if err := validate.Validate(msg, now); err != nil {
		t.Fatalf("Validate: %v, want nil", err)
	}
}

func TestValidateRejectsUnsupportedSchemaMajor(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	msg := validMessage(now)
	msg.Variants[0].SchemaVersion = model.SchemaVersion{Major: model.CurrentMajor + 1}

	err := validate.Validate(msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a schema version error")
	}
	if !strings.Contains(err.Error(), "schema major") {
		t.Errorf("Validate error = %q, want it to mention the unsupported major", err)
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

	err := validate.Validate(msg, now)
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

	err := validate.Validate(msg, now)
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

	err := validate.Validate(msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a content security error from the sequence step")
	}
}
