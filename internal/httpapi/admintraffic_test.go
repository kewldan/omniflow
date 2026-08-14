package httpapi

import (
	"testing"

	"github.com/omniflow/omniflow/internal/remnawave"
)

// The node ordering exists to answer one question — which node is filling up —
// and the answer has to be at the top. A node with no limit cannot be filling
// up, so it belongs below every node that can, however much it has moved.
func TestNodesUnderPressureSortFirst(t *testing.T) {
	lines := nodeLines([]remnawave.Node{
		// Enormous absolute use, no limit: not under pressure.
		{Name: "unlimited", TrafficUsedBytes: 900_000_000_000},
		{Name: "quiet", TrafficUsedBytes: 10, TrafficLimitBytes: 100},
		{Name: "full", TrafficUsedBytes: 95, TrafficLimitBytes: 100},
		{Name: "half", TrafficUsedBytes: 50, TrafficLimitBytes: 100},
	})

	order := make([]string, 0, len(lines))
	for _, line := range lines {
		order = append(order, line.Name)
	}
	want := []string{"full", "half", "quiet", "unlimited"}
	for index, name := range want {
		if order[index] != name {
			t.Fatalf("nodes ordered %v, want %v", order, want)
		}
	}

	// A node with no limit reports no share rather than zero, so it is never
	// coloured as if it were empty when it simply has nothing to fill.
	for _, line := range lines {
		if line.Name == "unlimited" && line.UsedShare != nil {
			t.Fatalf("an unlimited node reported a saturation of %v", *line.UsedShare)
		}
		if line.Name == "full" {
			if line.UsedShare == nil || *line.UsedShare != 0.95 {
				t.Fatalf("saturation is %v, want 0.95", line.UsedShare)
			}
		}
	}
}
