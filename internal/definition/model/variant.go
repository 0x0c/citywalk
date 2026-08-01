package model

import (
	"encoding/json"
	"fmt"
)

// Variant is one renderable content object belonging to a Message, with a weight for the experiment
// split and a language tag (CW-0003 Unit 1). A message with one variant and one language has exactly
// one; splitting Variant out of Message is what makes an A/B test arm and a translation the same
// mechanism.
type Variant struct {
	ID            string
	MessageID     string
	Weight        int
	Language      string
	SchemaVersion SchemaVersion
	Content       Content
}

// variantContentWire is the JSON shape persisted in the variants.content column (CW-0003 Unit 3):
// the version alongside the tagged-union content document, so a reader can tell which schema a
// document was written against without also decoding the content.
type variantContentWire struct {
	SchemaVersion SchemaVersion   `json:"schema_version"`
	Content       json.RawMessage `json:"content"`
}

// MarshalContentColumn encodes v's SchemaVersion and Content into the document stored in the
// variants.content column.
func (v Variant) MarshalContentColumn() ([]byte, error) {
	contentJSON, err := MarshalContent(v.Content)
	if err != nil {
		return nil, fmt.Errorf("model: marshal variant %s content: %w", v.ID, err)
	}
	return json.Marshal(variantContentWire{SchemaVersion: v.SchemaVersion, Content: contentJSON})
}

// UnmarshalContentColumn decodes the variants.content column into v's SchemaVersion and Content.
func (v *Variant) UnmarshalContentColumn(data []byte) error {
	var wire variantContentWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("model: decode variant %s content column: %w", v.ID, err)
	}
	content, err := UnmarshalContent(wire.Content)
	if err != nil {
		return fmt.Errorf("model: decode variant %s content: %w", v.ID, err)
	}
	v.SchemaVersion = wire.SchemaVersion
	v.Content = content
	return nil
}
