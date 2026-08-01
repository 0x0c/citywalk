package model

import (
	"encoding/json"
	"fmt"
)

// Layout discriminates the Content tagged union (CW-0003 Unit 2): one member per display form.
type Layout string

const (
	LayoutDialog     Layout = "dialog"
	LayoutBanner     Layout = "banner"
	LayoutFullScreen Layout = "full_screen"
	LayoutHTML       Layout = "html"
	LayoutSequence   Layout = "sequence"
)

// Content is one renderable member of the tagged union a Variant carries. Rendering half of an
// unknown member is worse than rendering nothing, so a decoder that meets an unrecognized Layout
// rejects the whole document rather than passing through what it does understand (CW-0003 Unit 4).
type Content interface {
	Layout() Layout
}

// MediaRef points at a content-addressed asset (CW-0010 Unit 7); the definition service stores the
// reference, never the asset bytes.
type MediaRef struct {
	URL string `json:"url"`
}

// Colors is the pair every presented member needs: what the surface is drawn on and what the text is
// drawn in.
type Colors struct {
	Background string `json:"background"`
	Text       string `json:"text"`
}

// Presentation is the field set CW-0003 Unit 2 names as shared: heading, body, media reference,
// buttons, colors, corner radius, and the automatic-close delay. It is declared once and embedded by
// every Content member that presents on screen, rather than repeated per member.
type Presentation struct {
	Heading               string    `json:"heading"`
	Body                  string    `json:"body"`
	Media                 *MediaRef `json:"media,omitempty"`
	Buttons               []Button  `json:"buttons,omitempty"`
	Colors                Colors    `json:"colors"`
	CornerRadius          float64   `json:"corner_radius"`
	AutoCloseDelaySeconds *int      `json:"auto_close_delay_seconds,omitempty"`
}

type DialogContent struct {
	Presentation
}

func (DialogContent) Layout() Layout { return LayoutDialog }

// Edge is the screen edge a BannerContent is anchored to.
type Edge string

const (
	EdgeTop    Edge = "top"
	EdgeBottom Edge = "bottom"
)

type BannerContent struct {
	Presentation
	Edge Edge `json:"edge"`
}

func (BannerContent) Layout() Layout { return LayoutBanner }

type FullScreenContent struct {
	Presentation
}

func (FullScreenContent) Layout() Layout { return LayoutFullScreen }

// HTMLContent is arbitrary HTML content (CW-0003 Unit 2), the one member that renders its own layout
// rather than composing Presentation.
type HTMLContent struct {
	HTML string `json:"html"`
}

func (HTMLContent) Layout() Layout { return LayoutHTML }

// SequenceContent presents several screens in order, such as a survey or an onboarding flow. Each
// step is itself a DialogContent or a FullScreenContent (CW-0003 Unit 2); a step of any other Layout
// is a decode error.
type SequenceContent struct {
	Steps []Content
}

func (SequenceContent) Layout() Layout { return LayoutSequence }

func (c SequenceContent) MarshalJSON() ([]byte, error) {
	steps := make([]json.RawMessage, len(c.Steps))
	for i, step := range c.Steps {
		raw, err := MarshalContent(step)
		if err != nil {
			return nil, fmt.Errorf("model: marshal sequence step %d: %w", i, err)
		}
		steps[i] = raw
	}
	return json.Marshal(struct {
		Layout Layout            `json:"layout"`
		Steps  []json.RawMessage `json:"steps"`
	}{Layout: LayoutSequence, Steps: steps})
}

func (c *SequenceContent) UnmarshalJSON(data []byte) error {
	var wire struct {
		Steps []json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("model: decode sequence: %w", err)
	}
	steps := make([]Content, len(wire.Steps))
	for i, raw := range wire.Steps {
		step, err := UnmarshalContent(raw)
		if err != nil {
			return fmt.Errorf("model: decode sequence step %d: %w", i, err)
		}
		if step.Layout() != LayoutDialog && step.Layout() != LayoutFullScreen {
			return fmt.Errorf("model: sequence step %d has layout %q, want dialog or full_screen", i, step.Layout())
		}
		steps[i] = step
	}
	c.Steps = steps
	return nil
}

// layoutSniff reads only the discriminator, so UnmarshalContent can dispatch to the right concrete
// type before decoding the rest.
type layoutSniff struct {
	Layout Layout `json:"layout"`
}

// MarshalContent encodes c into the tagged-union wire shape.
func MarshalContent(c Content) ([]byte, error) {
	switch v := c.(type) {
	case DialogContent, BannerContent, FullScreenContent, HTMLContent, SequenceContent:
		return marshalWithLayout(v, c.Layout())
	default:
		return nil, fmt.Errorf("model: unknown content type %T", c)
	}
}

// marshalWithLayout marshals v (a struct with no layout field of its own, SequenceContent excepted —
// it has a bespoke MarshalJSON already) and injects the "layout" discriminator into the result.
func marshalWithLayout(v any, layout Layout) ([]byte, error) {
	if seq, ok := v.(SequenceContent); ok {
		return seq.MarshalJSON()
	}
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("model: marshal content: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("model: marshal content: %w", err)
	}
	layoutJSON, err := json.Marshal(layout)
	if err != nil {
		return nil, fmt.Errorf("model: marshal content: %w", err)
	}
	fields["layout"] = layoutJSON
	return json.Marshal(fields)
}

// UnmarshalContent decodes the tagged-union wire shape, rejecting any Layout this build does not
// know (CW-0003 Unit 4's save-time half of the compatibility policy: the server never persists a
// document its own delivery path cannot later serve).
func UnmarshalContent(data []byte) (Content, error) {
	var sniff layoutSniff
	if err := json.Unmarshal(data, &sniff); err != nil {
		return nil, fmt.Errorf("model: decode content: %w", err)
	}
	switch sniff.Layout {
	case LayoutDialog:
		var c DialogContent
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("model: decode dialog content: %w", err)
		}
		return c, nil
	case LayoutBanner:
		var c BannerContent
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("model: decode banner content: %w", err)
		}
		return c, nil
	case LayoutFullScreen:
		var c FullScreenContent
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("model: decode full_screen content: %w", err)
		}
		return c, nil
	case LayoutHTML:
		var c HTMLContent
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("model: decode html content: %w", err)
		}
		return c, nil
	case LayoutSequence:
		var c SequenceContent
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		return c, nil
	default:
		return nil, fmt.Errorf("model: unknown layout %q", sniff.Layout)
	}
}
