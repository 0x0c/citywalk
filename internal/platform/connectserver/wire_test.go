package connectserver

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	adminv1 "github.com/0x0c/citywalk/gen/citywalk/admin/v1"
	eventv1 "github.com/0x0c/citywalk/gen/citywalk/event/v1"
	"github.com/0x0c/citywalk/internal/definition/model"
	eventmodel "github.com/0x0c/citywalk/internal/event/model"
)

func fixtureMessage() model.Message {
	return model.Message{
		ID:       "message-1",
		Name:     "Kamakura route promo",
		State:    model.MessageStateActive,
		Priority: 10,
		Window: model.Window{
			Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		},
		Version:         3,
		AudienceRef:     "segment-1",
		HoldoutFraction: 0.1,
		ExperimentSalt:  "salt-1",
		ConversionEvent: "route_started",
		ControlPolicy: model.ControlPolicy{
			PerMessageCap:              5,
			MinIntervalBetween:         2 * time.Hour,
			ExemptFromProjectCap:       true,
			RequiresServerConfirmation: true,
		},
		Triggers: []model.Trigger{
			{ID: "trigger-1", Kind: "event", OccurrenceGoal: 3, EventPredicate: `properties.screen == "routes"`},
		},
		DisplayConditions: []model.DisplayCondition{
			{Delay: 5 * time.Second, ScreenFilterMode: model.ScreenFilterDeny, Screens: []string{"checkout"}, ConnectivityRequired: model.ConnectivityAny},
		},
		Variants: []model.Variant{
			{
				ID: "variant-ja", Weight: 60, Language: "ja",
				SchemaVersion: model.SchemaVersion{Major: 1, Minor: 2},
				Content:       model.DialogContent{Presentation: model.Presentation{Heading: "こんにちは"}},
			},
			{
				ID: "variant-en", Weight: 40, Language: "en",
				SchemaVersion: model.SchemaVersion{Major: 1, Minor: 0},
				Content:       model.DialogContent{Presentation: model.Presentation{Heading: "Hello"}},
			},
		},
	}
}

// TestVariantsWireRoundTrip covers the projection every administrative response and request passes
// variants through. The content column is the part with real structure — a tagged union carrying its
// own schema version — so a round trip that lost the layout or the version would put a variant on the
// wire that no device could render.
func TestVariantsWireRoundTrip(t *testing.T) {
	want := fixtureMessage().Variants

	data, err := marshalVariants(want)
	if err != nil {
		t.Fatalf("marshalVariants: %v", err)
	}
	got, err := unmarshalVariants(data)
	if err != nil {
		t.Fatalf("unmarshalVariants: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("unmarshalVariants returned %d variants, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Weight != want[i].Weight || got[i].Language != want[i].Language {
			t.Errorf("variant %d = %+v, want ID/Weight/Language of %+v", i, got[i], want[i])
		}
		if got[i].SchemaVersion != want[i].SchemaVersion {
			t.Errorf("variant %d SchemaVersion = %+v, want %+v", i, got[i].SchemaVersion, want[i].SchemaVersion)
		}
		dialog, ok := got[i].Content.(model.DialogContent)
		if !ok {
			t.Fatalf("variant %d Content = %T, want model.DialogContent", i, got[i].Content)
		}
		if dialog.Heading != want[i].Content.(model.DialogContent).Heading {
			t.Errorf("variant %d Heading = %q, want %q", i, dialog.Heading, want[i].Content.(model.DialogContent).Heading)
		}
	}
}

// TestUnmarshalVariantsOnAnEmptyColumnIsEmpty is the shape a message saved before it had variants
// leaves behind: an absent column is no variants, not a decode failure that would make the whole
// message unreadable.
func TestUnmarshalVariantsOnAnEmptyColumnIsEmpty(t *testing.T) {
	got, err := unmarshalVariants(nil)
	if err != nil {
		t.Fatalf("unmarshalVariants(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unmarshalVariants(nil) = %v, want none", got)
	}
}

func TestUnmarshalVariantsRejectsMalformedJSON(t *testing.T) {
	if _, err := unmarshalVariants([]byte("{not json")); err == nil {
		t.Fatal("unmarshalVariants: got nil error for malformed JSON, want a rejection")
	}
}

// TestMessageDefinitionRoundTrip walks a fully populated message out to the administrative wire shape
// and back. Every nested entity travels as JSON, so a field dropped in either direction is invisible
// at the protobuf layer — the round trip is what catches it.
func TestMessageDefinitionRoundTrip(t *testing.T) {
	want := fixtureMessage()

	wire, err := toMessageDefinition(want)
	if err != nil {
		t.Fatalf("toMessageDefinition: %v", err)
	}
	got, err := fromMessageDefinition(wire)
	if err != nil {
		t.Fatalf("fromMessageDefinition: %v", err)
	}

	if got.Name != want.Name || got.State != want.State || got.Priority != want.Priority {
		t.Errorf("name/state/priority = %q/%q/%d, want %q/%q/%d", got.Name, got.State, got.Priority, want.Name, want.State, want.Priority)
	}
	if !got.Window.Start.Equal(want.Window.Start) || !got.Window.End.Equal(want.Window.End) {
		t.Errorf("window = %v..%v, want %v..%v", got.Window.Start, got.Window.End, want.Window.Start, want.Window.End)
	}
	if got.AudienceRef != want.AudienceRef || got.HoldoutFraction != want.HoldoutFraction || got.ConversionEvent != want.ConversionEvent {
		t.Errorf("audience/holdout/conversion = %q/%v/%q, want %q/%v/%q",
			got.AudienceRef, got.HoldoutFraction, got.ConversionEvent, want.AudienceRef, want.HoldoutFraction, want.ConversionEvent)
	}
	if got.ControlPolicy != want.ControlPolicy {
		t.Errorf("ControlPolicy = %+v, want %+v", got.ControlPolicy, want.ControlPolicy)
	}
	if len(got.Triggers) != 1 || got.Triggers[0] != want.Triggers[0] {
		t.Errorf("Triggers = %+v, want %+v", got.Triggers, want.Triggers)
	}
	if len(got.DisplayConditions) != 1 {
		t.Fatalf("DisplayConditions = %+v, want one", got.DisplayConditions)
	}
	gotCondition, wantCondition := got.DisplayConditions[0], want.DisplayConditions[0]
	if gotCondition.Delay != wantCondition.Delay || gotCondition.ScreenFilterMode != wantCondition.ScreenFilterMode ||
		gotCondition.ConnectivityRequired != wantCondition.ConnectivityRequired ||
		len(gotCondition.Screens) != 1 || gotCondition.Screens[0] != wantCondition.Screens[0] {
		t.Errorf("DisplayConditions[0] = %+v, want %+v", gotCondition, wantCondition)
	}
	if len(got.Variants) != len(want.Variants) {
		t.Errorf("Variants = %d, want %d", len(got.Variants), len(want.Variants))
	}
}

// TestFromMessageDefinitionToleratesAbsentNestedDocuments is the minimal message an administrative
// client can legitimately send: no control policy, no triggers, no display conditions, no variants.
// Each absent field has to decode to its zero value rather than fail, since "" is what an unset bytes
// field arrives as.
func TestFromMessageDefinitionToleratesAbsentNestedDocuments(t *testing.T) {
	got, err := fromMessageDefinition(&adminv1.MessageDefinition{
		Name:        "minimal",
		State:       string(model.MessageStateDraft),
		WindowStart: timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		WindowEnd:   timestamppb.New(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("fromMessageDefinition: %v", err)
	}
	if got.Name != "minimal" || got.State != model.MessageStateDraft {
		t.Errorf("name/state = %q/%q, want %q/%q", got.Name, got.State, "minimal", model.MessageStateDraft)
	}
	if len(got.Triggers) != 0 || len(got.DisplayConditions) != 0 || len(got.Variants) != 0 {
		t.Errorf("nested entities = %d triggers, %d conditions, %d variants; want none",
			len(got.Triggers), len(got.DisplayConditions), len(got.Variants))
	}
	if (got.ControlPolicy != model.ControlPolicy{}) {
		t.Errorf("ControlPolicy = %+v, want the zero policy", got.ControlPolicy)
	}
}

func TestFromMessageDefinitionRejectsMalformedNestedJSON(t *testing.T) {
	tests := map[string]*adminv1.MessageDefinition{
		"control_policy":     {ControlPolicyJson: []byte("{not json")},
		"triggers":           {TriggersJson: []byte("{not json")},
		"display_conditions": {DisplayConditionsJson: []byte("{not json")},
		"variants":           {VariantsJson: []byte("{not json")},
	}
	for name, wire := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := fromMessageDefinition(wire); err == nil {
				t.Fatalf("fromMessageDefinition with malformed %s: got nil error, want a rejection", name)
			}
		})
	}
}

// TestFromWireCarriesEveryFieldTheModelValidates is the seam between the device's submission and
// CW-0009's own Event validation: a field dropped here would be rejected as missing by Validate, or
// worse, accepted with a silently empty message or variant that no report could ever attribute.
func TestFromWireCarriesEveryFieldTheModelValidates(t *testing.T) {
	deviceTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	got, err := fromWire(&eventv1.Event{
		Id:                "event-1",
		ChannelId:         "channel-1",
		Kind:              string(eventmodel.KindImpression),
		Name:              "impression",
		DeviceTime:        timestamppb.New(deviceTime),
		PropertiesJson:    []byte(`{"placement":"banner","step":2}`),
		MessageId:         "message-1",
		VariantId:         "variant-1",
		SuppressionReason: string(eventmodel.ReasonProjectBudget),
	})
	if err != nil {
		t.Fatalf("fromWire: %v", err)
	}

	if got.ID != "event-1" || got.ChannelID != "channel-1" || got.Kind != eventmodel.KindImpression {
		t.Errorf("id/channel/kind = %q/%q/%q, want event-1/channel-1/%q", got.ID, got.ChannelID, got.Kind, eventmodel.KindImpression)
	}
	if !got.DeviceTime.Equal(deviceTime) {
		t.Errorf("DeviceTime = %v, want %v", got.DeviceTime, deviceTime)
	}
	if got.MessageID != "message-1" || got.VariantID != "variant-1" {
		t.Errorf("message/variant = %q/%q, want message-1/variant-1", got.MessageID, got.VariantID)
	}
	if got.SuppressionReason != eventmodel.ReasonProjectBudget {
		t.Errorf("SuppressionReason = %q, want %q", got.SuppressionReason, eventmodel.ReasonProjectBudget)
	}
	if got.Properties["placement"] != "banner" {
		t.Errorf("Properties = %v, want placement=banner", got.Properties)
	}
}

// TestFromWireLeavesPropertiesNilWhenTheDeviceSendsNone keeps an event with no properties from
// arriving as an empty-but-present map, which the log would then store as {} rather than the null the
// column defaults to — a difference no report reads, but one that would make otherwise identical
// events compare unequal.
func TestFromWireLeavesPropertiesNilWhenTheDeviceSendsNone(t *testing.T) {
	got, err := fromWire(&eventv1.Event{Id: "event-1", ChannelId: "channel-1", Kind: string(eventmodel.KindImpression)})
	if err != nil {
		t.Fatalf("fromWire: %v", err)
	}
	if got.Properties != nil {
		t.Errorf("Properties = %v, want nil", got.Properties)
	}
}

func TestFromWireRejectsMalformedProperties(t *testing.T) {
	if _, err := fromWire(&eventv1.Event{Id: "event-1", PropertiesJson: []byte("{not json")}); err == nil {
		t.Fatal("fromWire: got nil error for malformed properties_json, want a rejection")
	}
}
