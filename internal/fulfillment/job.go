package fulfillment

import "github.com/riverqueue/river"

type JobArgs struct {
	OperationID string `json:"operationId" river:"unique"`
}

func (JobArgs) Kind() string { return "fulfillment" }

func InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Queue: "critical", MaxAttempts: 12, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}
