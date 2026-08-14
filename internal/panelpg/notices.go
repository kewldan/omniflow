package panelpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/notice"
)

// Rewording the notices the installation sends on its own initiative.
//
// `/v1/panel/marketing/templates` already covers campaigns, where an operator
// writes the body and a campaign sends it. The transactional notices — expiry,
// traffic, renewal, grace, recovery, fulfillment, dunning — were compiled in,
// which meant the one voice every customer hears repeatedly was the one nobody
// could change without a release.
//
// Everything about this is shaped by the fact that nobody reads the text
// between the operator writing it and a customer receiving it:
//
//   - Values are named, not positional. `fmt.Sprintf` prints
//     `%!d(string=Pro)` for a verb that meets the wrong type and does not fail,
//     so it reaches the customer looking broken.
//   - A placeholder the notice does not carry is refused when it is saved.
//   - So is markup Telegram would reject, which would otherwise turn into a
//     delivery failure hours later with nobody watching.
//   - A preview renders against sample values, so an operator judging whether
//     the wording fits is judging a message rather than a template.
//
// And an override is an exception rather than a copy: no row means the shipped
// wording, so an installation that reverts today keeps getting improved
// defaults after every upgrade.

// Notice is one overridable message as the panel shows it.
type Notice struct {
	Code      string            `json:"code"`
	Variables []notice.Variable `json:"variables"`
	// Default is the shipped wording per locale, which is what an override
	// replaces and what reverting restores.
	Default map[string]string `json:"default"`
	// Overrides holds only the locales an operator has actually reworded.
	Overrides map[string]NoticeOverride `json:"overrides,omitempty"`
}

// NoticeOverride is one operator-authored body.
type NoticeOverride struct {
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
}

// NoticePreview is a body rendered against sample values.
type NoticePreview struct {
	Rendered string `json:"rendered"`
	// Placeholders is what the body actually used, which is how an operator
	// notices that they deleted `{days}` from the expiry warning.
	Placeholders []string `json:"placeholders"`
}

// Notices lists every overridable notice with whatever has been written for it.
func (service *Service) Notices(ctx context.Context) ([]Notice, error) {
	rows, err := service.queries().ListNoticeOverrides(ctx)
	if err != nil {
		return nil, err
	}
	written := make(map[string]map[string]NoticeOverride, len(rows))
	for _, row := range rows {
		if written[row.Code] == nil {
			written[row.Code] = map[string]NoticeOverride{}
		}
		written[row.Code][row.Locale] = NoticeOverride{
			Body: row.Body, UpdatedAt: row.UpdatedAt.Time,
			UpdatedBy: uuidString(row.UpdatedBy),
		}
	}

	definitions := notice.Definitions()
	notices := make([]Notice, 0, len(definitions))
	for _, definition := range definitions {
		notices = append(notices, Notice{
			Code:      string(definition.Code),
			Variables: definition.Variables,
			Default:   definition.Default,
			Overrides: written[string(definition.Code)],
		})
	}
	return notices, nil
}

// SaveNotice stores one operator-authored body.
//
// Validation happens before the write and against the definition, so a body
// that would render `{name}` as literal braces or send markup Telegram refuses
// never reaches the table. The refusal names what is wrong, because "invalid"
// on a screen with a text area is not something anybody can act on.
func (service *Service) SaveNotice(
	ctx context.Context, code, locale, body string, actor Actor,
) (NoticeOverride, error) {
	definition, err := resolveNotice(code, locale)
	if err != nil {
		return NoticeOverride{}, err
	}
	body = strings.TrimSpace(body)
	if err := notice.Check(definition, body); err != nil {
		return NoticeOverride{}, wrapValidation(err)
	}

	var saved NoticeOverride
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, saveErr := queries.SaveNoticeOverride(ctx, dbgen.SaveNoticeOverrideParams{
			Code: code, Locale: locale, Body: body,
			UpdatedBy: optionalUUID(actor.AdminID),
		})
		if saveErr != nil {
			return saveErr
		}
		saved = NoticeOverride{
			Body: row.Body, UpdatedAt: row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		}
		// The body is not in the audit payload. It is up to 2000 characters of
		// message text, it is readable from the table it was just written to,
		// and an audit trail is a record of who did what rather than a second
		// copy of the content.
		return appendAudit(ctx, queries, actor.audit(
			"notice.overridden", "configuration", "notice", code+"/"+locale,
			map[string]any{"length": len([]rune(body))},
		))
	})
	return saved, err
}

// RevertNotice removes an override, restoring the shipped wording.
//
// Reverting something that was never overridden is not an error. The operator
// asked for the default and the default is what they have.
func (service *Service) RevertNotice(
	ctx context.Context, code, locale string, actor Actor,
) error {
	if _, err := resolveNotice(code, locale); err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, deleteErr := queries.DeleteNoticeOverride(ctx, dbgen.DeleteNoticeOverrideParams{
			Code: code, Locale: locale,
		})
		if deleteErr != nil {
			return deleteErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"notice.reverted", "configuration", "notice", code+"/"+locale,
			map[string]any{"removed": removed == 1},
		))
	})
}

// PreviewNotice renders a body against the notice's sample values.
//
// It validates first and refuses the same things a save refuses, so the preview
// cannot show an operator a message that could not be stored. The samples come
// from the definition rather than from this screen, so what is previewed here
// is what a test send would produce.
func (service *Service) PreviewNotice(
	_ context.Context, code, locale, body string,
) (NoticePreview, error) {
	definition, err := resolveNotice(code, locale)
	if err != nil {
		return NoticePreview{}, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		// An empty editor previews the shipped wording, which is what the
		// customer would receive if the operator saved nothing.
		body = definition.Default[locale]
	}
	if err := notice.Check(definition, body); err != nil {
		return NoticePreview{}, wrapValidation(err)
	}
	return NoticePreview{
		Rendered:     notice.Render(body, notice.Samples(definition)),
		Placeholders: notice.Placeholders(body),
	}, nil
}

// NoticeTest is one queued copy of a notice for the operator group.
type NoticeTest struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Locale    string    `json:"locale"`
	Status    string    `json:"status"`
	ErrorCode string    `json:"errorCode,omitempty"`
	Requested time.Time `json:"requestedAt"`
	Resolved  time.Time `json:"resolvedAt,omitzero"`
}

// SendNoticeTest queues one rendered copy of a notice for the operator group.
//
// The preview above it renders in a browser, which is worth having and is not
// the same thing: a browser cannot show that Telegram accepts the markup, how
// the emoji look on a phone, or where the lines break. This can.
//
// It goes to the operator group and never to a customer. A transactional notice
// has a trigger — a subscription about to expire, a charge that failed — and
// manufacturing one against a real customer to see how it reads would be a lie
// told to somebody entitled to believe their subscription is in trouble.
//
// The body is rendered and stored now rather than resolved at delivery. What
// the operator asked to see is the text in the editor at this moment, which may
// not be saved and may be edited again before the group receives it.
func (service *Service) SendNoticeTest(
	ctx context.Context, code, locale, body string, actor Actor,
) (NoticeTest, error) {
	preview, err := service.PreviewNotice(ctx, code, locale, body)
	if err != nil {
		return NoticeTest{}, err
	}

	var test NoticeTest
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.EnqueueNoticeTestSend(ctx, dbgen.EnqueueNoticeTestSendParams{
			Code: code, Locale: locale, Body: preview.Rendered,
			RequestedBy: optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return txErr
		}
		test = NoticeTest{
			ID: uuidString(row.ID), Code: row.Code, Locale: row.Locale, Status: row.Status,
			Requested: row.RequestedAt.Time,
		}
		return appendAudit(ctx, queries, actor.audit(
			"notice.tested", "configuration", "notice", code+"/"+locale,
			map[string]any{"testSendId": test.ID},
		))
	})
	return test, err
}

// NoticeTests lists what has been sent to the operators for one notice, so the
// screen can show a test still in flight rather than a button that did nothing.
func (service *Service) NoticeTests(
	ctx context.Context, code string, limit int32,
) ([]NoticeTest, error) {
	if _, ok := notice.Lookup(strings.TrimSpace(code)); !ok {
		return nil, wrapValidation(fmt.Errorf("%w: %q", notice.ErrUnknownNotice, code))
	}
	rows, err := service.queries().ListNoticeTestSends(ctx, dbgen.ListNoticeTestSendsParams{
		Code: code, PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, err
	}
	tests := make([]NoticeTest, 0, len(rows))
	for _, row := range rows {
		tests = append(tests, NoticeTest{
			ID: uuidString(row.ID), Code: row.Code, Locale: row.Locale, Status: row.Status,
			ErrorCode: row.ErrorCode.String, Requested: row.RequestedAt.Time,
			Resolved: instant(row.ResolvedAt),
		})
	}
	return tests, nil
}

// resolveNotice rejects an unknown code or locale as a validation failure
// rather than a 404: the request named something that is not overridable, which
// is a statement about the request rather than about a missing record.
func resolveNotice(code, locale string) (notice.Definition, error) {
	definition, ok := notice.Lookup(strings.TrimSpace(code))
	if !ok {
		return notice.Definition{}, wrapValidation(
			fmt.Errorf("%w: %q", notice.ErrUnknownNotice, code))
	}
	if _, ok := definition.Default[locale]; !ok {
		return notice.Definition{}, wrapValidation(
			errors.New("a notice is written in en or ru"))
	}
	return definition, nil
}
