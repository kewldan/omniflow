//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	telegram "github.com/go-telegram/bot"
	"github.com/omniflow/omniflow/internal/operator"
)

// groupStub is a Telegram that answers the operator group's calls and records
// each method with its form values.
type groupStub struct {
	mutex sync.Mutex
	calls []map[string]string
}

func (stub *groupStub) methods(name string) []map[string]string {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	var found []map[string]string
	for _, call := range stub.calls {
		if call["method"] == name {
			found = append(found, call)
		}
	}
	return found
}

func newGroupStub(t *testing.T) (*groupStub, *telegram.Bot) {
	t.Helper()
	stub := &groupStub{}
	thread := 100
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseMultipartForm(1 << 20)
		call := map[string]string{"method": request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]}
		for key, values := range request.Form {
			call[key] = values[0]
		}
		stub.mutex.Lock()
		stub.calls = append(stub.calls, call)
		thread++
		current := thread
		stub.mutex.Unlock()
		var result any
		switch call["method"] {
		case "getForumTopicIconStickers":
			result = []map[string]any{
				{"file_id": "a", "file_unique_id": "a", "type": "custom_emoji", "width": 1, "height": 1,
					"is_animated": false, "is_video": false, "emoji": "🤖", "custom_emoji_id": "robot-icon"},
				{"file_id": "b", "file_unique_id": "b", "type": "custom_emoji", "width": 1, "height": 1,
					"is_animated": false, "is_video": false, "emoji": "❗", "custom_emoji_id": "bang-icon"},
			}
		case "createForumTopic":
			result = map[string]any{"message_thread_id": current, "name": call["name"], "icon_color": 0}
		case "sendMessage", "editMessageText":
			result = map[string]any{"message_id": 500, "date": 0, "chat": map[string]any{"id": -1001, "type": "supergroup"}}
		default:
			result = true
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(server.Close)
	client, err := telegram.New("test-token", telegram.WithServerURL(server.URL), telegram.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	return stub, client
}

// Topics are created with a plain name and their icon; the startup report is
// one pinned message, edited on the next start instead of posted again; and a
// repair renames topics in place with their icons.
func TestTheOperatorGroupKeepsOneStatusMessageAndPlainTopics(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	stub, client := newGroupStub(t)
	notifier := operator.New(harness.pool, client, slog.New(slog.DiscardHandler),
		operator.Config{ChatID: -1001, PanelURL: "https://lk.example.test", Locale: "en"})

	if err := notifier.EnsureTopics(ctx); err != nil {
		t.Fatal(err)
	}
	created := stub.methods("createForumTopic")
	if len(created) != len(operator.Kinds) {
		t.Fatalf("created %d topics, want %d", len(created), len(operator.Kinds))
	}
	byName := map[string]map[string]string{}
	for _, call := range created {
		byName[call["name"]] = call
	}
	if byName["Infrastructure"]["icon_custom_emoji_id"] != "robot-icon" ||
		byName["Fulfillment failures"]["icon_custom_emoji_id"] != "bang-icon" {
		t.Fatalf("icons not set: %+v", byName["Infrastructure"])
	}
	if len(stub.methods("getForumTopicIconStickers")) != 1 {
		t.Fatal("the icon list is read once and cached")
	}

	notifier.SetStartReport(operator.StartReport{Name: "Omniflow", Version: "1.0.0", SalesMode: "open"})
	notifier.PostStatus(ctx)
	if len(stub.methods("sendMessage")) != 1 || len(stub.methods("pinChatMessage")) != 1 {
		t.Fatalf("first start: %d sent, %d pinned", len(stub.methods("sendMessage")), len(stub.methods("pinChatMessage")))
	}
	if text := stub.methods("sendMessage")[0]["text"]; !strings.Contains(text, "Omniflow · v1.0.0") {
		t.Fatalf("status text:\n%s", text)
	}

	// The next start edits the same message.
	notifier.SetStartReport(operator.StartReport{Name: "Omniflow", Version: "1.0.1", SalesMode: "open"})
	notifier.PostStatus(ctx)
	edits := stub.methods("editMessageText")
	if len(stub.methods("sendMessage")) != 1 || len(edits) != 1 || edits[0]["message_id"] != "500" ||
		!strings.Contains(edits[0]["text"], "v1.0.1") {
		t.Fatalf("second start: %d sent, edits %+v", len(stub.methods("sendMessage")), edits)
	}
	// Nothing waits once posted.
	notifier.PostStatus(ctx)
	if len(stub.methods("editMessageText")) != 1 {
		t.Fatal("a posted report was posted again")
	}

	// A repair renames topics in place, a few per pass.
	if _, err := harness.pool.Exec(ctx, `UPDATE operator_topics SET needs_repair = true WHERE status = 'bound'`); err != nil {
		t.Fatal(err)
	}
	for range len(operator.Kinds) {
		notifier.RepairTopics(ctx)
	}
	repaired := stub.methods("editForumTopic")
	if len(repaired) != len(operator.Kinds) {
		t.Fatalf("repaired %d topics, want %d", len(repaired), len(operator.Kinds))
	}
	var left int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_topics WHERE needs_repair`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("topics still marked for repair: %d, %v", left, err)
	}
}
