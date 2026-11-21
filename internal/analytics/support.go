package analytics

import (
	"context"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// RatingSettle is how long after a ticket is resolved the customer is asked
// for a mark. A resolution an operator reverses a minute later should not
// produce a question about it.
const RatingSettle = 10 * time.Minute

// RatingRequest is a resolved ticket whose customer is due a question.
type RatingRequest struct {
	TicketID   string
	CustomerID string
	Subject    string
}

// PendingRatingRequests lists tickets resolved in the last three days whose
// customer has not been asked yet.
func (service *Service) PendingRatingRequests(ctx context.Context, limit int32) ([]RatingRequest, error) {
	rows, err := service.queries().PendingRatingRequests(ctx, dbgen.PendingRatingRequestsParams{
		SettleSeconds: RatingSettle.Seconds(), BatchSize: limit,
	})
	if err != nil {
		return nil, err
	}
	requests := make([]RatingRequest, 0, len(rows))
	for _, row := range rows {
		requests = append(requests, RatingRequest{
			TicketID: uuidString(row.TicketID), CustomerID: uuidString(row.UserID), Subject: row.Subject,
		})
	}
	return requests, nil
}

// MarkRatingRequested records that the question went out, or that it could
// not: either way it is not asked again.
func (service *Service) MarkRatingRequested(ctx context.Context, ticketID string) error {
	id, err := parseUUID(ticketID)
	if err != nil {
		return err
	}
	return service.queries().MarkRatingRequested(ctx, id)
}

// RecordRating stores the customer's mark on their own ticket, once. It
// reports whether the mark was taken: false for a ticket that is not theirs
// or that already has one.
func (service *Service) RecordRating(ctx context.Context, customerID, ticketID string, rating int) (bool, error) {
	if rating < 1 || rating > 5 {
		return false, invalid("a mark is from 1 to 5")
	}
	customer, err := parseUUID(customerID)
	if err != nil {
		return false, err
	}
	ticket, err := parseUUID(ticketID)
	if err != nil {
		return false, err
	}
	updated, err := service.queries().RecordTicketRating(ctx, dbgen.RecordTicketRatingParams{
		Rating: int16(rating), TicketID: ticket, UserID: customer,
	})
	return updated > 0, err
}

// OperatorSatisfaction is one operator's marks.
type OperatorSatisfaction struct {
	OperatorID  string   `json:"operatorId"`
	DisplayName string   `json:"displayName"`
	Ratings     int64    `json:"ratings"`
	Csat        *float64 `json:"csat,omitempty"`
	Average     *float64 `json:"average,omitempty"`
}

// SupportSatisfaction is customer satisfaction with support over a window.
type SupportSatisfaction struct {
	Since     time.Time `json:"since"`
	Until     time.Time `json:"until"`
	Requested int64     `json:"requested"`
	Ratings   int64     `json:"ratings"`
	// ResponseRate is marks received over questions sent in the window. The
	// two sets overlap rather than coincide — a question sent on the last day
	// may be answered the next — so it is an indication, not a cohort.
	ResponseRate *float64               `json:"responseRate,omitempty"`
	Csat         *float64               `json:"csat,omitempty"`
	Average      *float64               `json:"average,omitempty"`
	Distribution [5]int64               `json:"distribution"`
	Operators    []OperatorSatisfaction `json:"operators"`
}

// SupportSatisfaction reads the marks received in the period.
func (service *Service) SupportSatisfaction(ctx context.Context, period Period) (SupportSatisfaction, error) {
	queries := service.queries()
	result := SupportSatisfaction{Since: period.Since, Until: period.Until, Operators: []OperatorSatisfaction{}}
	row, err := queries.SupportSatisfaction(ctx, dbgen.SupportSatisfactionParams{
		Since: timestamp(period.Since), Until: timestamp(period.Until),
	})
	if err != nil {
		return result, err
	}
	result.Requested, result.Ratings = row.Requested, row.Ratings
	result.ResponseRate = rate(row.Ratings, row.Requested)
	result.Csat = rate(row.Satisfied, row.Ratings)
	if row.Ratings > 0 {
		average := row.Average
		result.Average = &average
	}
	result.Distribution = [5]int64{row.R1, row.R2, row.R3, row.R4, row.R5}
	operators, err := queries.SupportSatisfactionByOperator(ctx, dbgen.SupportSatisfactionByOperatorParams{
		Since: timestamp(period.Since), Until: timestamp(period.Until),
	})
	if err != nil {
		return result, err
	}
	for _, operator := range operators {
		line := OperatorSatisfaction{
			OperatorID: uuidString(operator.OperatorID), DisplayName: operator.DisplayName,
			Ratings: operator.Ratings, Csat: rate(operator.Satisfied, operator.Ratings),
		}
		if operator.Ratings > 0 {
			average := operator.Average
			line.Average = &average
		}
		result.Operators = append(result.Operators, line)
	}
	return result, nil
}
