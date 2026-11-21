// Package bedolagaimport decides how the records of a Bedolaga bot database
// become Omniflow customers, wallet balances, subscriptions, and referral
// pairs.
//
// It is the pure half of cmd/migrate-bedolaga: it reads nothing and writes
// nothing. The reader in internal/bedolagasource fills Source, the
// store in internal/bedolagaimportpg fills Target and carries out the Plan,
// and everything in between — what conflicts, what is skipped, which plan a
// tariff becomes — is decided here, where it can be tested without a database.
package bedolagaimport

import "time"

// User is one row of Bedolaga's `users` table, reduced to what migrates.
type User struct {
	ID              int64
	TelegramID      *int64
	Username        string
	FirstName       string
	LastName        string
	Status          string
	Language        string
	BalanceKopeks   int64
	Email           string
	EmailVerified   bool
	EmailVerifiedAt *time.Time
	ReferredByID    *int64
	ReferralCode    string
	HasPaid         bool
	RemnawaveID     *int64
	CreatedAt       time.Time
	// OAuth maps an Omniflow sign-in provider slug (google, yandex, discord,
	// vk) to the subject Bedolaga stored for this user, so a customer who
	// signed into the Bedolaga cabinet that way lands on the same account.
	OAuth map[string]string
}

// OAuthIssuers is the issuer each carried sign-in provider's identities are
// bound to; the slugs are those of Omniflow's provider presets.
var OAuthIssuers = map[string]string{
	"google":  "https://accounts.google.com",
	"yandex":  "https://oauth.yandex.ru",
	"discord": "https://discord.com",
	"vk":      "https://id.vk.ru",
}

// Bedolaga user statuses.
const (
	SourceStatusActive  = "active"
	SourceStatusBlocked = "blocked"
	SourceStatusDeleted = "deleted"
)

// Subscription is one row of Bedolaga's `subscriptions` table.
type Subscription struct {
	ID          int64
	UserID      int64
	Status      string
	IsTrial     bool
	StartDate   time.Time
	EndDate     time.Time
	TariffID    *int64
	RemnawaveID *int64
	CreatedAt   time.Time
}

// Payment is one completed top-up in Bedolaga's `transactions` table: money a
// customer paid in. Bedolaga sells everything from the balance, so a top-up is
// the moment money arrived; the purchases made from it are not payments.
type Payment struct {
	ID           int64
	UserID       int64
	AmountKopeks int64
	// Method is Bedolaga's payment_method, empty when it recorded none.
	Method string
	PaidAt time.Time
}

// Tariff is one row of Bedolaga's `tariffs` table.
type Tariff struct {
	ID       int64
	Name     string
	IsActive bool
}

// RemoteUser is what the Remnawave panel currently holds for one user. The
// importer only ever reads it; the panel is the authority for access, and an
// imported entitlement mirrors it so the first reconciliation changes nothing.
type RemoteUser struct {
	ID                int64
	Username          string
	Status            string
	ExpireAt          time.Time
	TrafficLimitBytes int64
	DeviceLimit       *int
	SquadIDs          []string
	TelegramID        *int64
}

// CountSum is one bucket of a report-only aggregate: how many records, and
// the sum of their amounts in kopeks where the records carry one.
type CountSum struct {
	Key      string `json:"key"`
	Count    int64  `json:"count"`
	SumMinor int64  `json:"sumMinor"`
}

// Archive is what stays behind in the Bedolaga dump. These records are
// counted for the operator and left in the archived database rather than
// rewritten into Omniflow's ledger as if Omniflow had taken those payments.
// Completed top-ups are carried separately, as legacy payments: history that
// segments, loyalty, and reports read, never ledger entries.
type Archive struct {
	Transactions       []CountSum `json:"transactions"`
	CompletedDeposits  []CountSum `json:"completedDeposits"`
	StarsOrders        []CountSum `json:"starsOrders"`
	Tickets            []CountSum `json:"tickets"`
	ActivePromoCodes   []CountSum `json:"activePromoCodes"`
	PromoGroups        []CountSum `json:"promoGroups"`
	ReferralEarnings   CountSum   `json:"referralEarnings"`
	WithdrawalRequests []CountSum `json:"withdrawalRequests"`
	OAuthLinkedUsers   int64      `json:"oauthLinkedUsers"`
	MissingTables      []string   `json:"missingTables,omitempty"`
}

// Source is everything read from Bedolaga for one run.
type Source struct {
	Users         []User
	Subscriptions []Subscription
	Tariffs       []Tariff
	Payments      []Payment
	Archive       Archive
}
