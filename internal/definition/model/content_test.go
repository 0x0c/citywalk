package model_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/definition/model"
)

// TestContentRoundTrip demonstrates the shape of CW-0003 Unit 2's tagged union surviving a save and
// a later read: every member marshals with its layout discriminator and unmarshals back to the same
// concrete type carrying the same fields.
func TestContentRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		content model.Content
	}{
		{
			name: "dialog with a button carrying two actions",
			content: model.DialogContent{
				Presentation: model.Presentation{
					Heading: "Route complete",
					Body:    "You just finished a 5 km walk.",
					Colors:  model.Colors{Background: "#FFFFFF", Text: "#000000"},
					Buttons: []model.Button{
						{
							Label: "Share",
							Actions: []model.Action{
								model.EmitEventAction{EventName: "route_share_tapped"},
								model.NavigateAction{Destination: "share_sheet"},
							},
						},
					},
				},
			},
		},
		{
			name: "banner anchored to the top edge",
			content: model.BannerContent{
				Presentation: model.Presentation{Heading: "Maintenance in 1 hour"},
				Edge:         model.EdgeTop,
			},
		},
		{
			name:    "full screen with no buttons",
			content: model.FullScreenContent{Presentation: model.Presentation{Heading: "Welcome"}},
		},
		{
			name:    "arbitrary html",
			content: model.HTMLContent{HTML: "<p>Hello</p>"},
		},
		{
			name: "sequence of two dialog steps",
			content: model.SequenceContent{
				Steps: []model.Content{
					model.DialogContent{Presentation: model.Presentation{Heading: "Step 1"}},
					model.FullScreenContent{Presentation: model.Presentation{Heading: "Step 2"}},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := model.MarshalContent(tc.content)
			if err != nil {
				t.Fatalf("MarshalContent: %v", err)
			}

			got, err := model.UnmarshalContent(data)
			if err != nil {
				t.Fatalf("UnmarshalContent: %v", err)
			}

			if got.Layout() != tc.content.Layout() {
				t.Errorf("Layout() = %v, want %v", got.Layout(), tc.content.Layout())
			}

			roundTripped, err := model.MarshalContent(got)
			if err != nil {
				t.Fatalf("MarshalContent (round-tripped): %v", err)
			}
			if string(roundTripped) != string(data) {
				t.Errorf("round-tripped JSON differs:\n got: %s\nwant: %s", roundTripped, data)
			}
		})
	}
}

// TestUnmarshalContentRejectsUnknownLayout demonstrates CW-0003 Unit 4: a document declaring a
// layout this build does not know is a decode error, never a best-effort pass-through.
func TestUnmarshalContentRejectsUnknownLayout(t *testing.T) {
	_, err := model.UnmarshalContent([]byte(`{"layout":"holographic_projection"}`))
	if err == nil {
		t.Fatal("UnmarshalContent: got nil error for an unknown layout, want an error")
	}
}

// TestUnmarshalContentRejectsSequenceStepOfWrongLayout demonstrates CW-0003 Unit 2: a sequence's
// steps must be dialog or full_screen — a banner step is a decode error.
func TestUnmarshalContentRejectsSequenceStepOfWrongLayout(t *testing.T) {
	bannerData, err := model.MarshalContent(model.BannerContent{Edge: model.EdgeTop})
	if err != nil {
		t.Fatalf("MarshalContent (banner): %v", err)
	}
	tampered := []byte(`{"layout":"sequence","steps":[` + string(bannerData) + `]}`)

	_, err = model.UnmarshalContent(tampered)
	if err == nil {
		t.Fatal("UnmarshalContent: got nil error for a banner sequence step, want an error")
	}
}

// TestUnmarshalActionRejectsUnknownKind demonstrates the same reject-rather-than-guess rule applies
// to the Action union a Button carries.
func TestUnmarshalActionRejectsUnknownKind(t *testing.T) {
	_, err := model.UnmarshalAction([]byte(`{"kind":"self_destruct"}`))
	if err == nil {
		t.Fatal("UnmarshalAction: got nil error for an unknown kind, want an error")
	}
}
