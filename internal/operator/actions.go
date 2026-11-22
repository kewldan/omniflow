package operator

import "strings"

// Buttons that act from the operator group.
//
// A notice's callback button carries "opn:<action>:<id>". The bot answers it
// (internal/botapp, operator_actions.go): it checks the chat and the person,
// runs the action through the same service the panel uses, and writes the
// audit entry. Every action here is idempotent — closing a closed ticket or
// blocking a blocked customer changes nothing — and blocking asks first.

// ActionPrefix starts every operator-notice callback.
const ActionPrefix = "opn:"

// The actions a notice button can carry.
const (
	// ActionCloseTicket closes a support ticket.
	ActionCloseTicket = "tclose"
	// ActionBlock asks to confirm blocking a customer.
	ActionBlock = "block"
	// ActionBlockConfirm blocks (suspends) a customer.
	ActionBlockConfirm = "blockok"
	// ActionBlockCancel puts the block button back.
	ActionBlockCancel = "blockno"
	// ActionUnblock reactivates a blocked customer.
	ActionUnblock = "unblock"
	// ActionRetryFulfillment puts a failed delivery back in the queue now.
	ActionRetryFulfillment = "fretry"
	// ActionDone marks a button whose action already ran; it does nothing.
	ActionDone = "done"
)

// ActionData is the callback data of an action button. Telegram allows 64
// bytes; the longest action with a UUID is 47.
func ActionData(action, id string) string {
	return ActionPrefix + action + ":" + id
}

// ParseAction reads callback data written by ActionData.
func ParseAction(data string) (action, id string, ok bool) {
	rest, found := strings.CutPrefix(data, ActionPrefix)
	if !found {
		return "", "", false
	}
	action, id, found = strings.Cut(rest, ":")
	if !found || action == "" {
		return "", "", false
	}
	return action, id, true
}
