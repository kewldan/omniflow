package botapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	telegram "github.com/go-telegram/bot"
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
	member, answered := app.membershipNow(context.Background(), nil, pgtype.UUID{}, channel, known, 42, now)
	if !member || !answered {
		t.Fatalf("a fresh cached membership was not used: %v %v", member, answered)
	}
	if stub.calls != 0 {
		t.Fatalf("telegram was called despite a fresh cached answer: %d", stub.calls)
	}
}

// A stale answer is re-asked — and so is a fresh absence, see
// channel_recheck_test.go — because somebody who just joined should not be
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
	member, answered := app.membershipNow(context.Background(), nil, pgtype.UUID{}, channel, known, 42, now)
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
		context.Background(), nil, pgtype.UUID{}, channel, nil, 42, clockFixture())
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

// A muted member is still a member: taking somebody's subscription away
// because a moderator silenced them is a consequence nobody intended. But a
// restricted account that has left the chat keeps the `restricted` status with
// `is_member` false, and it used to count as a member all the same. The answer
// is read the way Telegram sends it, through the real client.
func TestRestrictedCountsAsMembershipOnlyWhileInTheChat(t *testing.T) {
	answers := map[string]string{
		"creator":            `{"status":"creator","user":{"id":1},"is_anonymous":false}`,
		"member":             `{"status":"member","user":{"id":1}}`,
		"restricted present": `{"status":"restricted","user":{"id":1},"is_member":true,"until_date":0}`,
		"restricted left":    `{"status":"restricted","user":{"id":1},"is_member":false,"until_date":0}`,
		"left":               `{"status":"left","user":{"id":1}}`,
		"kicked":             `{"status":"kicked","user":{"id":1},"until_date":0}`,
	}
	expected := map[string]bool{
		"creator": true, "member": true, "restricted present": true,
		"restricted left": false, "left": false, "kicked": false,
	}
	for name, answer := range answers {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ok":true,"result":` + answer + `}`))
		}))
		client, err := telegram.New("test-token", telegram.WithServerURL(server.URL), telegram.WithSkipGetMe())
		if err != nil {
			t.Fatalf("telegram client: %v", err)
		}
		member, err := NewTelegramMembership(client).IsMember(context.Background(), -100, 1)
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if member != expected[name] {
			t.Fatalf("%s counted as member = %v, want %v", name, member, expected[name])
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
