package platform

import (
	"fmt"

	"github.com/valkey-io/valkey-go"
)

func NewValkeyClient(rawURL string) (valkey.Client, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("valkey URL is required")
	}
	options := valkey.MustParseURL(rawURL)
	client, err := valkey.NewClient(options)
	if err != nil {
		return nil, fmt.Errorf("connect to valkey: %w", err)
	}
	return client, nil
}
