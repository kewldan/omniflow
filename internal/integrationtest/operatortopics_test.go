//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// The panel shows the topics the bot made and can ask for them again: every
// topic goes back to pending with no thread, for the bot to create on its
// next pass, and the request is audited.
func TestOperatorTopicsCanBeRecreatedFromThePanel(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO operator_topics (kind, chat_id, topic_id, status, last_error_code) VALUES
		('purchase', -1001, 11, 'bound', NULL), ('support', -1001, NULL, 'failed', 'missing_manage_topics_permission')`); err != nil {
		t.Fatal(err)
	}
	topics, err := panel.OperatorTopics(ctx)
	if err != nil || len(topics) != 2 {
		t.Fatalf("topics = %+v, %v", topics, err)
	}
	if topics[0].Kind != "purchase" || topics[0].TopicID == nil || *topics[0].TopicID != 11 ||
		topics[1].ErrorCode != "missing_manage_topics_permission" {
		t.Fatalf("topics = %+v", topics)
	}

	actor := harness.operator(ctx, t, "topics@example.test")
	// Repair marks the bound topics for the bot to rename in place.
	marked, err := panel.RepairOperatorTopics(ctx, actor)
	if err != nil || marked != 1 {
		t.Fatalf("repair = %d, %v", marked, err)
	}
	topics, _ = panel.OperatorTopics(ctx)
	if !topics[0].Repairing || topics[1].Repairing {
		t.Fatalf("only the bound topic is repaired: %+v", topics)
	}
	reset, err := panel.RecreateOperatorTopics(ctx, actor)
	if err != nil || reset != 2 {
		t.Fatalf("recreate = %d, %v", reset, err)
	}
	topics, _ = panel.OperatorTopics(ctx)
	for _, topic := range topics {
		if topic.Status != "pending" || topic.TopicID != nil || topic.ErrorCode != "" {
			t.Fatalf("not reset: %+v", topic)
		}
	}
	var audited int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action = 'panel.operator_topics.recreate_requested' AND actor_id = $1`, actor.AdminID).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit rows = %d, %v", audited, err)
	}
}
