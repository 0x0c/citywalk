// Package validate implements CW-0003 Unit 5's save-time validation: the definition service rejects
// a malformed Message rather than warning about it, because a rejection at save reaches the person
// who can fix it while a rejection at delivery reaches a device and nobody at all.
//
// Referential integrity covers two of the three references CW-0003 Unit 5 names: a non-empty
// AudienceRef must name a real row in segments (CW-0004/CW-0005's audience service), and a non-nil
// Presentation.Media must be a content-addressed reference CW-0010 Unit 7's object storage package
// recognizes the shape of. The conversion event catalog is still not checked — it has no owner yet,
// so it exists as no queryable store at all, unlike media, whose store now exists (CW-0010 Unit 7)
// even though nothing in this codebase's default configuration talks to a live one. Validate
// otherwise covers everything this pass can check on its own: structural shape (enforced by model's
// typed decode), temporal sanity, variant weights, and content security.
//
// Media referential integrity is checked as shape only, never as store existence, and that is a
// deliberate choice rather than the cheaper option taken by default. Existence would need a reachable
// object storage endpoint on every message save — turning a definition save, which today depends on
// nothing but Postgres, into a request that fails whenever an object store is unreachable, even for a
// deployment that has not turned phase two on (CW-0010 Unit 11's config-gated staging: the store's
// reachability is exactly the thing that must not be assumed on a request path that doesn't already
// use it). Shape checking still catches the actual failure mode this gap names — an arbitrary string
// masquerading as a media reference — without adding that dependency; it does not catch a reference
// that is well-formed but names an object nobody ever uploaded, which is a real gap this pass accepts.
// objectstorage.Client.Exists provides the stronger check for a caller willing to pay for it outside
// the save path (see that method's own doc comment).
package validate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/platform/objectstorage"
)

// allowedLinkSchemes is the allowlist CW-0003 Unit 5 requires for OpenLinkAction.URL. HTTPS only: a
// campaign author has no legitimate reason to open a plain-HTTP or custom-scheme URL from inside the
// application, and either widens the content-injection surface Unit 5 exists to close.
var allowedLinkSchemes = map[string]bool{
	"https://": true,
}

// forbiddenHTMLSchemes are the executable URI schemes CW-0003 Unit 5 names as a rejection when they
// appear in HTMLContent: each can run script in the context of the page displaying it.
var forbiddenHTMLSchemes = []string{
	"javascript:",
	"vbscript:",
	"data:text/html",
}

// Validate runs every save-time check this pass implements against msg, evaluated as of now, and
// returns a joined error naming every violation found — the caller decides how to surface that to
// the campaign author, but Validate itself never returns a partial pass. pool is used only for the
// referential integrity check (AudienceRef existence); every other check is pure.
func Validate(ctx context.Context, pool *pgxpool.Pool, msg model.Message, now time.Time) error {
	var errs []error

	if err := validateTemporalSanity(msg.Window, now); err != nil {
		errs = append(errs, err)
	}
	if err := validateVariantWeights(msg.Variants); err != nil {
		errs = append(errs, err)
	}
	for _, v := range msg.Variants {
		if err := validateSchemaVersion(v); err != nil {
			errs = append(errs, err)
		}
		if err := validateContentSecurity(v); err != nil {
			errs = append(errs, err)
		}
		if err := validateMediaRef(v); err != nil {
			errs = append(errs, err)
		}
	}
	if err := validateAudienceRef(ctx, pool, msg.AudienceRef); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// validateAudienceRef rejects a non-empty AudienceRef that names no row in segments. An empty
// AudienceRef is not itself a validation failure here — a message reaching everyone (no segment
// restriction) is a legitimate, if unusual, thing to save; whether that's allowed is a policy
// decision for whatever calls Validate, not this function's to make.
func validateAudienceRef(ctx context.Context, pool *pgxpool.Pool, audienceRef string) error {
	if audienceRef == "" {
		return nil
	}
	var exists bool
	// SELECT EXISTS always returns exactly one row, so the only error QueryRow can produce here is a
	// real query failure — never pgx.ErrNoRows.
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM segments WHERE id = $1)`, audienceRef).Scan(&exists); err != nil {
		return fmt.Errorf("check audience_ref %q: %w", audienceRef, err)
	}
	if !exists {
		return fmt.Errorf("audience_ref %q does not name an existing segment", audienceRef)
	}
	return nil
}

// validateTemporalSanity checks the window's start precedes its end, and the end is in the future —
// a message whose window has already closed is not worth saving as an active campaign.
func validateTemporalSanity(w model.Window, now time.Time) error {
	var errs []error
	if !w.Start.Before(w.End) {
		errs = append(errs, fmt.Errorf("window start %s must precede end %s", w.Start, w.End))
	}
	if !w.End.After(now) {
		errs = append(errs, fmt.Errorf("window end %s must be in the future (now %s)", w.End, now))
	}
	return errors.Join(errs...)
}

// expectedVariantWeightTotal is the total a language group's variant weights must sum to. Weights
// are a percentage split among the variants competing for that language's traffic (CW-0003 Unit 1):
// the delivery service picks a variant by language, then by experiment assignment among that
// language's weights, so each language's group must sum to a whole 100.
const expectedVariantWeightTotal = 100

// validateVariantWeights groups variants by language and requires each group's weights to sum to
// expectedVariantWeightTotal — the split the delivery service later assigns devices against.
func validateVariantWeights(variants []model.Variant) error {
	totals := make(map[string]int)
	for _, v := range variants {
		totals[v.Language] += v.Weight
	}
	var errs []error
	for language, total := range totals {
		if total != expectedVariantWeightTotal {
			errs = append(errs, fmt.Errorf(
				"variant weights for language %q sum to %d, want %d", language, total, expectedVariantWeightTotal,
			))
		}
	}
	return errors.Join(errs...)
}

// validateSchemaVersion rejects a variant declaring a Major this build does not currently admit —
// the server must never persist a document its own delivery path cannot later emit. During a
// major-version transition, model.SupportedMajors names more than just model.CurrentMajor, so a
// message can carry both the old Major's content and the new Major's content side by side, which is
// what gives payload.Build's parallel-emission selection (SchemaVersion.SupportsMajor) something to
// choose between (CW-0003 Unit 4).
func validateSchemaVersion(v model.Variant) error {
	if !model.SupportsSchemaMajor(v.SchemaVersion.Major) {
		return fmt.Errorf("variant %s declares schema major %d, this build supports %v",
			v.ID, v.SchemaVersion.Major, model.SupportedMajors)
	}
	return nil
}

// validateContentSecurity walks v's content for the link-scheme allowlist and the HTML
// executable-scheme rejection CW-0003 Unit 5 requires.
func validateContentSecurity(v model.Variant) error {
	var errs []error
	walkContent(v.Content, func(c model.Content) {
		switch content := c.(type) {
		case model.HTMLContent:
			lowerHTML := strings.ToLower(content.HTML)
			for _, scheme := range forbiddenHTMLSchemes {
				if strings.Contains(lowerHTML, scheme) {
					errs = append(errs, fmt.Errorf("variant %s HTML content contains forbidden scheme %q", v.ID, scheme))
				}
			}
		default:
			for _, button := range buttonsOf(c) {
				for _, action := range button.Actions {
					link, ok := action.(model.OpenLinkAction)
					if !ok {
						continue
					}
					if !hasAllowedScheme(link.URL) {
						errs = append(errs, fmt.Errorf("variant %s button %q opens URL with a disallowed scheme: %q", v.ID, button.Label, link.URL))
					}
				}
			}
		}
	})
	return errors.Join(errs...)
}

// validateMediaRef rejects a Presentation.Media whose URL is not a content-addressed reference
// objectstorage recognizes the shape of — CW-0003 Unit 5's media referential-integrity check. See
// this package's doc comment for why that stops at shape and does not confirm the object exists.
func validateMediaRef(v model.Variant) error {
	var errs []error
	walkContent(v.Content, func(c model.Content) {
		media := mediaOf(c)
		if media == nil {
			return
		}
		if !objectstorage.IsContentAddressedURL(media.URL) {
			errs = append(errs, fmt.Errorf(
				"variant %s media reference %q is not a content-addressed object storage URL", v.ID, media.URL,
			))
		}
	})
	return errors.Join(errs...)
}

// mediaOf returns the Media reference a presented Content member carries, or nil for a member with no
// Presentation (HTMLContent, SequenceContent — the latter's steps are walked separately) or one whose
// Media field is unset.
func mediaOf(c model.Content) *model.MediaRef {
	switch content := c.(type) {
	case model.DialogContent:
		return content.Media
	case model.BannerContent:
		return content.Media
	case model.FullScreenContent:
		return content.Media
	default:
		return nil
	}
}

// walkContent calls visit for c and, if c is a SequenceContent, for each of its steps.
func walkContent(c model.Content, visit func(model.Content)) {
	visit(c)
	if seq, ok := c.(model.SequenceContent); ok {
		for _, step := range seq.Steps {
			visit(step)
		}
	}
}

// buttonsOf returns the buttons a presented Content member carries, or nil for a member with no
// Presentation (HTMLContent, SequenceContent — the latter's steps are walked separately).
func buttonsOf(c model.Content) []model.Button {
	switch content := c.(type) {
	case model.DialogContent:
		return content.Buttons
	case model.BannerContent:
		return content.Buttons
	case model.FullScreenContent:
		return content.Buttons
	default:
		return nil
	}
}

// hasAllowedScheme reports whether url starts with one of allowedLinkSchemes,
// ASCII-case-insensitively — content security checks must not be defeated by an author writing
// "HTTPS://" instead of "https://".
func hasAllowedScheme(url string) bool {
	lower := strings.ToLower(url)
	for scheme := range allowedLinkSchemes {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	return false
}
