package remnawave

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// GeocheckJob is the state of one geocheck the panel ran on a node: the node
// asks geolocation databases, CDNs, and streaming and AI services how they
// see its address. Report is the node's report as the panel returns it, with
// the rendered image left out; nil until the job completes.
type GeocheckJob struct {
	Completed bool
	Failed    bool
	Success   bool
	Message   string
	Report    json.RawMessage
}

// RequestGeocheck queues a geocheck on a node and returns the job to poll.
// The node takes up to a minute to answer.
//
// The check runs on the node and changes nothing in the panel; Omniflow only
// reads its result.
func (client *Client) RequestGeocheck(ctx context.Context, nodeUUID string) (string, error) {
	var envelope struct {
		Response struct {
			JobID string `json:"jobId"`
		} `json:"response"`
	}
	path := "/api/connections/geocheck/" + url.PathEscape(nodeUUID)
	if err := client.do(ctx, http.MethodPost, path, struct{}{}, &envelope); err != nil {
		return "", err
	}
	return envelope.Response.JobID, nil
}

// Geocheck reads a queued geocheck.
func (client *Client) Geocheck(ctx context.Context, jobID string) (GeocheckJob, error) {
	var envelope struct {
		Response struct {
			IsCompleted bool `json:"isCompleted"`
			IsFailed    bool `json:"isFailed"`
			Result      *struct {
				Success   bool            `json:"success"`
				Message   *string         `json:"message"`
				RawReport json.RawMessage `json:"rawReport"`
			} `json:"result"`
		} `json:"response"`
	}
	if err := client.get(ctx, "/api/connections/geocheck/"+url.PathEscape(jobID), &envelope); err != nil {
		return GeocheckJob{}, err
	}
	job := GeocheckJob{Completed: envelope.Response.IsCompleted, Failed: envelope.Response.IsFailed}
	if result := envelope.Response.Result; result != nil {
		job.Success = result.Success
		if result.Message != nil {
			job.Message = *result.Message
		}
		if len(result.RawReport) > 0 && string(result.RawReport) != "null" {
			job.Report = result.RawReport
		}
	}
	return job, nil
}
