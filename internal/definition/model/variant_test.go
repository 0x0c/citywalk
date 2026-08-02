package model_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/definition/model"
)

// TestVariantContentColumnRoundTrip demonstrates the document CW-0003 Unit 3 stores in the
// variants.content column: the schema version travels with the content, so a reader can tell which
// schema a document was written against without decoding the content first.
func TestVariantContentColumnRoundTrip(t *testing.T) {
	v := model.Variant{
		ID:            "variant-1",
		MessageID:     "message-1",
		Weight:        100,
		Language:      "en",
		SchemaVersion: model.SchemaVersion{Major: 1, Minor: 2},
		Content: model.DialogContent{
			Presentation: model.Presentation{Heading: "Hello"},
		},
	}

	data, err := v.MarshalContentColumn()
	if err != nil {
		t.Fatalf("MarshalContentColumn: %v", err)
	}

	var got model.Variant
	if err := got.UnmarshalContentColumn(data); err != nil {
		t.Fatalf("UnmarshalContentColumn: %v", err)
	}

	if got.SchemaVersion != v.SchemaVersion {
		t.Errorf("SchemaVersion = %+v, want %+v", got.SchemaVersion, v.SchemaVersion)
	}
	dialog, ok := got.Content.(model.DialogContent)
	if !ok {
		t.Fatalf("Content type = %T, want model.DialogContent", got.Content)
	}
	if dialog.Heading != "Hello" {
		t.Errorf("Heading = %q, want %q", dialog.Heading, "Hello")
	}
}
