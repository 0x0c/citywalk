// Package store persists model.Message against the schema migrations/0002_definition_schema.sql
// creates (CW-0003 Unit 3). It implements only what proves that schema round-trips a full
// definition; the administrative create/update/delete/publish surface belongs to the definition
// service CW-0001 Unit 1 builds on top of this.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/definition/model"
)

// ErrNotFound is returned, wrapped, when GetMessage finds no row for the given ID.
var ErrNotFound = errors.New("store: not found")

// InsertMessage writes msg and every entity it owns — its ControlPolicy, Triggers,
// DisplayConditions, and Variants — in one transaction, and reports the generated ID back onto msg.
// The caller is responsible for calling validate.Validate first; InsertMessage assumes msg is valid.
func InsertMessage(ctx context.Context, pool *pgxpool.Pool, msg *model.Message) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if msg.Version == 0 {
		msg.Version = 1
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO messages (name, state, priority, window_start, window_end, audience_ref, holdout_fraction, conversion_event, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, experiment_salt
	`, msg.Name, string(msg.State), msg.Priority, msg.Window.Start, msg.Window.End, nullIfEmpty(msg.AudienceRef), msg.HoldoutFraction, nullIfEmpty(msg.ConversionEvent), msg.Version,
	).Scan(&msg.ID, &msg.ExperimentSalt); err != nil {
		return fmt.Errorf("store: insert message: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO control_policies (message_id, per_message_cap, min_interval_seconds, exempt_from_project_cap, requires_server_confirmation)
		VALUES ($1, $2, $3, $4, $5)
	`, msg.ID, msg.ControlPolicy.PerMessageCap, int(msg.ControlPolicy.MinIntervalBetween.Seconds()), msg.ControlPolicy.ExemptFromProjectCap, msg.ControlPolicy.RequiresServerConfirmation,
	); err != nil {
		return fmt.Errorf("store: insert control policy: %w", err)
	}

	for i, trg := range msg.Triggers {
		if err := tx.QueryRow(ctx, `
			INSERT INTO triggers (message_id, kind, occurrence_goal, event_predicate)
			VALUES ($1, $2, $3, $4)
			RETURNING id
		`, msg.ID, trg.Kind, trg.OccurrenceGoal, trg.EventPredicate,
		).Scan(&msg.Triggers[i].ID); err != nil {
			return fmt.Errorf("store: insert trigger %d: %w", i, err)
		}
	}

	for i, dc := range msg.DisplayConditions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO display_conditions (message_id, delay_seconds, screen_filter_mode, screens, connectivity_required)
			VALUES ($1, $2, $3, $4, $5)
		`, msg.ID, int(dc.Delay.Seconds()), string(dc.ScreenFilterMode), dc.Screens, string(dc.ConnectivityRequired),
		); err != nil {
			return fmt.Errorf("store: insert display condition %d: %w", i, err)
		}
	}

	for i := range msg.Variants {
		v := &msg.Variants[i]
		v.MessageID = msg.ID
		content, err := v.MarshalContentColumn()
		if err != nil {
			return fmt.Errorf("store: encode variant %d content: %w", i, err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO variants (message_id, weight, language, content)
			VALUES ($1, $2, $3, $4)
			RETURNING id
		`, msg.ID, v.Weight, v.Language, content,
		).Scan(&v.ID); err != nil {
			return fmt.Errorf("store: insert variant %d: %w", i, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// GetMessage reads back a Message and every entity InsertMessage wrote for it.
func GetMessage(ctx context.Context, pool *pgxpool.Pool, id string) (model.Message, error) {
	var msg model.Message
	var state string
	var audienceRef, conversionEvent *string
	if err := pool.QueryRow(ctx, `
		SELECT id, name, state, priority, window_start, window_end, audience_ref, holdout_fraction, conversion_event, version, experiment_salt
		FROM messages WHERE id = $1
	`, id).Scan(
		&msg.ID, &msg.Name, &state, &msg.Priority, &msg.Window.Start, &msg.Window.End,
		&audienceRef, &msg.HoldoutFraction, &conversionEvent, &msg.Version, &msg.ExperimentSalt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Message{}, fmt.Errorf("store: get message %s: %w", id, ErrNotFound)
		}
		return model.Message{}, fmt.Errorf("store: get message %s: %w", id, err)
	}
	msg.State = model.MessageState(state)
	msg.AudienceRef = stringOrEmpty(audienceRef)
	msg.ConversionEvent = stringOrEmpty(conversionEvent)

	var minIntervalSeconds int
	if err := pool.QueryRow(ctx, `
		SELECT per_message_cap, min_interval_seconds, exempt_from_project_cap, requires_server_confirmation
		FROM control_policies WHERE message_id = $1
	`, id).Scan(
		&msg.ControlPolicy.PerMessageCap, &minIntervalSeconds, &msg.ControlPolicy.ExemptFromProjectCap,
		&msg.ControlPolicy.RequiresServerConfirmation,
	); err != nil {
		return model.Message{}, fmt.Errorf("store: get control policy for message %s: %w", id, err)
	}
	msg.ControlPolicy.MinIntervalBetween = time.Duration(minIntervalSeconds) * time.Second

	triggerRows, err := pool.Query(ctx, `
		SELECT id, kind, occurrence_goal, event_predicate FROM triggers WHERE message_id = $1 ORDER BY id
	`, id)
	if err != nil {
		return model.Message{}, fmt.Errorf("store: query triggers for message %s: %w", id, err)
	}
	defer triggerRows.Close()
	for triggerRows.Next() {
		var trg model.Trigger
		if err := triggerRows.Scan(&trg.ID, &trg.Kind, &trg.OccurrenceGoal, &trg.EventPredicate); err != nil {
			return model.Message{}, fmt.Errorf("store: scan trigger for message %s: %w", id, err)
		}
		msg.Triggers = append(msg.Triggers, trg)
	}
	if err := triggerRows.Err(); err != nil {
		return model.Message{}, fmt.Errorf("store: read triggers for message %s: %w", id, err)
	}

	dcRows, err := pool.Query(ctx, `
		SELECT delay_seconds, screen_filter_mode, screens, connectivity_required
		FROM display_conditions WHERE message_id = $1 ORDER BY id
	`, id)
	if err != nil {
		return model.Message{}, fmt.Errorf("store: query display conditions for message %s: %w", id, err)
	}
	defer dcRows.Close()
	for dcRows.Next() {
		var dc model.DisplayCondition
		var delaySeconds int
		var screenFilterMode, connectivityRequired string
		if err := dcRows.Scan(&delaySeconds, &screenFilterMode, &dc.Screens, &connectivityRequired); err != nil {
			return model.Message{}, fmt.Errorf("store: scan display condition for message %s: %w", id, err)
		}
		dc.ScreenFilterMode = model.ScreenFilterMode(screenFilterMode)
		dc.ConnectivityRequired = model.ConnectivityRequirement(connectivityRequired)
		dc.Delay = time.Duration(delaySeconds) * time.Second
		msg.DisplayConditions = append(msg.DisplayConditions, dc)
	}
	if err := dcRows.Err(); err != nil {
		return model.Message{}, fmt.Errorf("store: read display conditions for message %s: %w", id, err)
	}

	variantRows, err := pool.Query(ctx, `
		SELECT id, message_id, weight, language, content FROM variants WHERE message_id = $1 ORDER BY id
	`, id)
	if err != nil {
		return model.Message{}, fmt.Errorf("store: query variants for message %s: %w", id, err)
	}
	defer variantRows.Close()
	for variantRows.Next() {
		var v model.Variant
		var content []byte
		if err := variantRows.Scan(&v.ID, &v.MessageID, &v.Weight, &v.Language, &content); err != nil {
			return model.Message{}, fmt.Errorf("store: scan variant for message %s: %w", id, err)
		}
		if err := v.UnmarshalContentColumn(content); err != nil {
			return model.Message{}, fmt.Errorf("store: decode variant content for message %s: %w", id, err)
		}
		msg.Variants = append(msg.Variants, v)
	}
	if err := variantRows.Err(); err != nil {
		return model.Message{}, fmt.Errorf("store: read variants for message %s: %w", id, err)
	}

	return msg, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
