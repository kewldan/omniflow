package accountreferral

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Program is the operator-configured invite scheme as the customer sees it.
//
// It is the same singleton the bot reads. The customer-facing fields are the
// promise being made — what an invite earns, how long it stays attributable, and
// where the terms live — and nothing here restates a rule that the reward worker
// does not actually enforce.
type Program struct {
	Enabled            bool
	Currency           string
	InviterRewardMinor int64
	InviteeRewardMinor int64
	// Qualification is what has to happen before a reward is granted. It is one
	// of a closed set the commerce code implements, so the panel can explain the
	// rule rather than paraphrasing it.
	Qualification string
	// InviterRewardCap bounds how many rewards one inviter may earn. Nil means
	// the operator set no cap.
	InviterRewardCap        *int
	AttributionValidityDays int
	RewardExpiryDays        *int
	TermsURL                string
}

// Summary is the referral screen in one response.
type Summary struct {
	Program Program

	// Code is the customer's own invite code. It is created on first read, which
	// is the same thing the bot does: the row is an identifier, not an
	// entitlement, and generating it lazily means a customer who never opens the
	// screen never gets one.
	Code string
	// Link is the shareable URL, empty when no public URL is configured.
	Link string
	// LinkReason explains an empty Link so the panel can say why instead of
	// rendering a share control that copies nothing.
	LinkReason string

	// Invited counts every attribution naming this customer as the inviter.
	// Qualified, Pending, and Rejected are each counted from the rows rather
	// than derived by subtraction, because an attribution can be qualified and
	// later rejected on review and a subtraction would report that pair twice.
	Invited   int64
	Qualified int64
	Pending   int64
	Rejected  int64

	// RewardedMinor is what the customer actually kept: reversed rewards are
	// excluded. The bot's older summary sums every row including reversals, which
	// overstates the balance for anyone whose reward was taken back, so the two
	// figures can differ for exactly those customers.
	RewardedMinor int64
	// ReversedMinor is what was granted and later taken back, carried separately
	// so a customer whose total dropped can see why rather than guess.
	ReversedMinor int64
	RewardCount   int
	// Currency denominates every amount in this summary, carried once rather
	// than repeated on each figure.
	Currency string
	// RemainingSlots is how many more rewarded invitations the cap allows. Nil
	// when the operator set no cap.
	RemainingSlots *int

	// Rewards is the first page of history, so the screen renders in one call.
	Rewards RewardPage
}

// Reward is one entry of the customer's reward history.
type Reward struct {
	ID   string
	Role string
	// State is pending, qualified, or rejected.
	//
	// It is derived from the reward row and the attribution behind it rather
	// than stored, because those are the two records that can move: a reward is
	// reversed on the reward row, and a pair is held or rejected on the
	// attribution. Reporting only one of them would show money a review has
	// already taken back.
	State       string
	AmountMinor int64
	Currency    string
	GrantedAt   time.Time
	ReversedAt  *time.Time
}

// RewardPage is one page of history plus the position after it.
type RewardPage struct {
	Items []Reward
	// NextCursor is empty on the last page.
	NextCursor string
}

// Reward states. The vocabulary is the customer's, not the database's: three
// words that answer "is this money mine?" without exposing review machinery.
const (
	RewardPending   = "pending"
	RewardQualified = "qualified"
	RewardRejected  = "rejected"
)

// Referrals reads the referral screen.
func (service *Service) Referrals(
	ctx context.Context, customerID, position string, limit int,
) (Summary, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return Summary{}, err
	}

	program, err := service.program(ctx)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{Program: program}

	code, err := newReferralCode()
	if err != nil {
		return Summary{}, err
	}
	// One statement so the code exists before the counts are taken against it.
	// The upsert is first-write-wins on `user_id`, so two tabs opening the screen
	// at once still produce one code.
	counts, err := service.queries.OpenAccountReferralSummary(ctx, dbgen.OpenAccountReferralSummaryParams{
		UserID: userID, Code: code,
	})
	if err != nil {
		return Summary{}, err
	}
	summary.Code = counts.Code
	summary.Invited = counts.Invited
	summary.Qualified = counts.Qualified
	summary.Rejected = counts.Rejected
	summary.Pending = counts.Pending
	summary.RewardedMinor = counts.RewardedMinor
	summary.ReversedMinor = counts.ReversedMinor
	summary.RewardCount = int(counts.RewardCount)

	summary.Currency = program.Currency
	if program.InviterRewardCap != nil {
		remaining := max(*program.InviterRewardCap-summary.RewardCount, 0)
		summary.RemainingSlots = &remaining
	}
	summary.Link, summary.LinkReason = ReferralLink(service.public, summary.Code)

	if summary.Rewards, err = service.rewards(ctx, userID, position, limit); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

// Rewards reads one page of reward history on its own, for the "load more"
// control that follows the first page embedded in the summary.
func (service *Service) Rewards(
	ctx context.Context, customerID, position string, limit int,
) (RewardPage, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return RewardPage{}, err
	}
	return service.rewards(ctx, userID, position, limit)
}

// rewards is the keyset page shared by both entry points.
//
// Ownership is in the query: `beneficiary_user_id` is the calling customer, so a
// forged cursor selects nothing rather than somebody else's history. Nothing in
// the projection carries the invited customer's identifier — an inviter is
// entitled to know that an invite paid out, not to a list of who their friends
// are once the friend is a customer of somebody else's service.
func (service *Service) rewards(
	ctx context.Context, userID pgtype.UUID, position string, limit int,
) (RewardPage, error) {
	size := pageSize(limit)
	keyset := decodeCursor(position)

	// One row past the page is read so the presence of a next page is known
	// without a second count query.
	rows, err := service.queries.ListAccountReferralRewards(ctx, dbgen.ListAccountReferralRewardsParams{
		UserID:          userID,
		CursorGrantedAt: keyset.timestamp(),
		CursorID:        keyset.uuid(),
		PageSize:        int32(size + 1),
	})
	if err != nil {
		return RewardPage{}, err
	}

	page := RewardPage{Items: make([]Reward, 0, size)}
	for _, row := range rows {
		if len(page.Items) == size {
			last := page.Items[size-1]
			page.NextCursor = encodeCursor(last.GrantedAt, last.ID)
			break
		}
		page.Items = append(page.Items, Reward{
			ID:          uuidString(row.ID),
			Role:        row.Role,
			State:       RewardState(row.ReversedAt.Valid, row.ReviewState),
			AmountMinor: row.AmountMinor,
			Currency:    row.Currency,
			GrantedAt:   row.GrantedAt.Time.UTC(),
			ReversedAt:  timePointer(row.ReversedAt),
		})
	}
	return page, nil
}

// RewardState maps a reward row and the review state of the pair behind it onto
// the three words the customer is shown.
//
// A reversal wins over everything: the money is gone and saying anything else
// would be a lie the balance contradicts. A held pair is pending rather than
// qualified because a person has not finished looking at it, and the customer is
// better served by "we are checking" than by a figure that may be withdrawn.
func RewardState(reversed bool, reviewState string) string {
	switch {
	case reversed, reviewState == "rejected":
		return RewardRejected
	case reviewState == "held":
		return RewardPending
	default:
		return RewardQualified
	}
}

// ReferralLink builds the shareable URL and, when it cannot, says why.
//
// An installation with no configured public URL gets an empty link and a reason
// rather than a link to nowhere: a share button that copies "/?ref=ABC" is worse
// than one that is visibly unavailable, because the customer only finds out
// after they have sent it to somebody.
func ReferralLink(publicURL, code string) (link, reason string) {
	base := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	switch {
	case code == "":
		return "", "no_code"
	case base == "":
		return "", "public_url_not_configured"
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "public_url_not_configured"
	}
	// The sign-in page is the landing point because a referral link is followed
	// by somebody who has no account yet. It carries the code as a query
	// parameter, which the panel replays on the first authenticated request.
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/account/sign-in"
	query := parsed.Query()
	query.Set("ref", code)
	parsed.RawQuery = query.Encode()
	return parsed.String(), ""
}

// program reads the singleton configuration.
//
// A missing row is the disabled default rather than an error: an installation
// that has never configured a programme still has to render the screen, and
// "off" is the honest rendering.
func (service *Service) program(ctx context.Context) (Program, error) {
	program := Program{
		Currency: "RUB", Qualification: "first_paid_order", AttributionValidityDays: 90,
	}
	row, err := service.queries.GetReferralProgram(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return program, nil
	}
	if err != nil {
		return Program{}, err
	}
	program.Enabled = row.Enabled
	program.Currency = row.Currency
	program.InviterRewardMinor = row.InviterRewardMinor
	program.InviteeRewardMinor = row.InviteeRewardMinor
	program.Qualification = row.Qualification
	program.AttributionValidityDays = int(row.AttributionValidityDays)
	if row.InviterRewardCap.Valid {
		value := int(row.InviterRewardCap.Int32)
		program.InviterRewardCap = &value
	}
	if row.RewardExpiryDays.Valid {
		value := int(row.RewardExpiryDays.Int32)
		program.RewardExpiryDays = &value
	}
	program.TermsURL = row.TermsUrl.String
	return program, nil
}

// newReferralCode mints the ten-character code the schema's check constraint
// requires. It matches the bot's generator exactly, so a code minted on either
// surface is indistinguishable and either surface can attribute it.
func newReferralCode() (string, error) {
	buffer := make([]byte, 7)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer)[:10], nil
}
