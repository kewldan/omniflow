package botapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/channelgate"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func clockFixture() time.Time {
	return time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
}

func channelFixture(invite, username string) dbgen.RequiredChannel {
	channel := dbgen.RequiredChannel{
		TelegramChatID: -100123, Title: "Announcements", RequireForPurchase: true,
	}
	if invite != "" {
		channel.InviteUrl = pgtype.Text{String: invite, Valid: true}
	}
	if username != "" {
		channel.Username = pgtype.Text{String: username, Valid: true}
	}
	return channel
}

// A requirement the customer cannot satisfy is a wall, so a channel with no way
// in is not enforced.
func TestAChannelWithNoWayInIsNotEnforced(t *testing.T) {
	if invite := inviteFor(channelFixture("", "")); invite != "" {
		t.Fatalf("a channel with no link produced one: %q", invite)
	}
	if invite := inviteFor(channelFixture("", "omniflow_news")); invite != "https://t.me/omniflow_news" {
		t.Fatalf("the username was not turned into a link: %q", invite)
	}
	// An explicit invite link wins, because a private channel has one and no
	// username at all.
	if invite := inviteFor(
		channelFixture("https://t.me/+abc123", "omniflow_news"),
	); invite != "https://t.me/+abc123" {
		t.Fatalf("the explicit invite link was not preferred: %q", invite)
	}
}

// stubVerifier answers whatever the test wants, including failing.
type stubVerifier struct {
	member bool
	err    error
	calls  int
}

func (stub *stubVerifier) IsMember(context.Context, int64, int64) (bool, error) {
	stub.calls++
	return stub.member, stub.err
}

// The periodic worker keeps the cache warm, and re-asking Telegram on every
// checkout is how a bot gets rate-limited at exactly the wrong moment.
func TestARecentCachedAnswerIsUsedWithoutCallingTelegram(t *testing.T) {
	stub := &stubVerifier{member: true}
	app := &App{membership: stub, logger: testLogger()}
	channel := channelFixture("https://t.me/x", "")
	channel.ID = pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	now := clockFixture()
	known := map[string]dbgen.ChannelMembership{
		uuidText(channel.ID): {
			State:     channelgate.StateMember,
			CheckedAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
		},
	}
	member, answered := app.membershipNow(context.Background(), channel, known, 42, now)
	if !member || !answered {
		t.Fatalf("a fresh cached membership was not used: %v %v", member, answered)
	}
	if stub.calls != 0 {
		t.Fatalf("telegram was called despite a fresh cached answer: %d", stub.calls)
	}
}

// A stale answer is re-asked, because somebody who just joined should not be
// told to join again.
func TestAStaleCachedAnswerIsRefreshed(t *testing.T) {
	stub := &stubVerifier{member: true}
	app := &App{membership: stub, logger: testLogger()}
	channel := channelFixture("https://t.me/x", "")
	channel.ID = pgtype.UUID{Bytes: [16]byte{2}, Valid: true}

	now := clockFixture()
	known := map[string]dbgen.ChannelMembership{
		uuidText(channel.ID): {
			State:     channelgate.StateAbsent,
			CheckedAt: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
		},
	}
	member, answered := app.membershipNow(context.Background(), channel, known, 42, now)
	if !member || !answered {
		t.Fatalf("the stale answer was used instead of a fresh one: %v %v", member, answered)
	}
	if stub.calls != 1 {
		t.Fatalf("telegram was not asked: %d", stub.calls)
	}
}

// Refusing a payment because Telegram was briefly unreachable would cost a real
// sale to enforce a marketing requirement.
func TestAnUnanswerableCheckPermitsThePurchase(t *testing.T) {
	stub := &stubVerifier{err: errors.New("telegram is unreachable")}
	app := &App{membership: stub, logger: testLogger()}
	channel := channelFixture("https://t.me/x", "")
	channel.ID = pgtype.UUID{Bytes: [16]byte{3}, Valid: true}

	member, answered := app.membershipNow(
		context.Background(), channel, nil, 42, clockFixture())
	if answered {
		t.Fatal("a failed call was reported as an answer")
	}
	if member {
		t.Fatal("a failed call was reported as membership")
	}
}

// An installation with no verifier gates nothing, which is what an installation
// with no configured channels wanted anyway.
func TestNoVerifierGatesNothing(t *testing.T) {
	app := &App{logger: testLogger()}
	if gate := app.checkPurchaseChannels(
		context.Background(), "00000000-0000-0000-0000-000000000001", 42,
	); !gate.Allowed() {
		t.Fatalf("a purchase was gated with no verifier: %+v", gate)
	}
}

// A gate with nothing missing is an allowed gate, so the caller has one thing
// to check rather than two.
func TestAnEmptyGateAllows(t *testing.T) {
	if !(PurchaseGate{}).Allowed() {
		t.Fatal("an empty gate blocked a purchase")
	}
	blocked := PurchaseGate{Missing: []ChannelRequirement{{Title: "News"}}}
	if blocked.Allowed() {
		t.Fatal("a gate with a missing channel allowed a purchase")
	}
}

// A muted member is still a member. Taking somebody's subscription away because
// a moderator silenced them is a consequence nobody intended.
func TestRestrictedCountsAsMembership(t *testing.T) {
	for status, expected := range map[string]bool{
		"creator": true, "administrator": true, "member": true, "restricted": true,
		"left": false, "kicked": false,
	} {
		if memberStatuses[status] != expected {
			t.Fatalf("%s counted as %v", status, memberStatuses[status])
		}
	}
}

// "Not a member" and "we could not ask" are different answers.
func TestADefiniteAbsenceIsNotAnOutage(t *testing.T) {
	if !isDefiniteAbsence(errors.New("Bad Request: user not found")) {
		t.Fatal("a definite absence was treated as an outage")
	}
	if isDefiniteAbsence(errors.New("context deadline exceeded")) {
		t.Fatal("a timeout was treated as a definite absence")
	}
}
