package remnawave

import (
	"context"
	"strconv"
	"time"
)

// DeviceStats is the panel's own device census.
type DeviceStats struct {
	ByPlatform []PlatformCount `json:"byPlatform"`
	Stats      struct {
		TotalUniqueDevices        int     `json:"totalUniqueDevices"`
		TotalHWIDDevices          int     `json:"totalHwidDevices"`
		AverageHWIDDevicesPerUser float64 `json:"averageHwidDevicesPerUser"`
	} `json:"stats"`
}

// PlatformCount is how many devices report one platform, and with which apps.
type PlatformCount struct {
	Platform string     `json:"platform"`
	Count    int        `json:"count"`
	ByApp    []AppCount `json:"byApp"`
}

// AppCount is how many devices of a platform use one client application.
type AppCount struct {
	App   string `json:"app"`
	Count int    `json:"count"`
}

// DeviceStats reads the panel's device and platform statistics.
func (client *Client) DeviceStats(ctx context.Context) (DeviceStats, error) {
	var envelope struct {
		Response DeviceStats `json:"response"`
	}
	err := client.get(ctx, "/api/hwid/devices/stats", &envelope)
	return envelope.Response, err
}

// SubscriptionRequest is one fetch of a user's subscription by a client app.
type SubscriptionRequest struct {
	RequestAt time.Time `json:"requestAt"`
	RequestIP string    `json:"requestIp"`
	UserAgent string    `json:"userAgent"`
}

// SubscriptionRequests reads the panel's record of when, from where, and by
// which application a user's subscription was fetched. Omniflow shows it to an
// operator and stores none of it.
func (client *Client) SubscriptionRequests(ctx context.Context, userID int64) ([]SubscriptionRequest, error) {
	var envelope struct {
		Response struct {
			Records []SubscriptionRequest `json:"records"`
		} `json:"response"`
	}
	err := client.get(ctx, "/api/users/"+strconv.FormatInt(userID, 10)+"/subscription-request-history", &envelope)
	return envelope.Response.Records, err
}
