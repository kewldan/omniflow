package botapp

import (
	"context"
	"strings"

	telegram "github.com/go-telegram/bot"
)

// TelegramMembership answers membership questions using the bot's own token.
//
// It lives here rather than in `internal/channelworker` because this is the
// process that holds the token, and the worker is deliberately an interface so
// it can run without one.
type TelegramMembership struct {
	client *telegram.Bot
}

// NewTelegramMembership wraps a bot client.
func NewTelegramMembership(client *telegram.Bot) *TelegramMembership {
	return &TelegramMembership{client: client}
}

// memberStatuses are the statuses that count as being in the channel.
//
// "restricted" counts: a muted member is still a member, and taking somebody's
// subscription away because a moderator silenced them in a channel would be a
// consequence nobody intended when they configured a marketing requirement.
var memberStatuses = map[string]bool{
	"creator": true, "administrator": true, "member": true, "restricted": true,
}

// IsMember reports whether an account is in a chat.
//
// An error means the question could not be answered, which callers treat
// differently from a "no": the worker records `unknown` and waits, and the
// purchase gate lets the sale through. Neither guesses.
func (membership *TelegramMembership) IsMember(
	ctx context.Context, chatID, telegramID int64,
) (bool, error) {
	member, err := membership.client.GetChatMember(ctx, &telegram.GetChatMemberParams{
		ChatID: chatID, UserID: telegramID,
	})
	if err != nil {
		// A customer who was never in the chat produces an error from some
		// Telegram deployments rather than a "left" status. That is a definite
		// answer, so it is reported as one instead of as an outage.
		if isDefiniteAbsence(err) {
			return false, nil
		}
		return false, err
	}
	if member == nil {
		return false, nil
	}
	return memberStatuses[string(member.Type)], nil
}

// isDefiniteAbsence recognises the Telegram errors that mean "not a member"
// rather than "we could not ask".
func isDefiniteAbsence(err error) bool {
	message := strings.ToLower(err.Error())
	for _, definite := range []string{
		"user not found", "participant_id_invalid", "chat member not found",
	} {
		if strings.Contains(message, definite) {
			return true
		}
	}
	return false
}
