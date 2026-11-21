package httpapi

import (
	"math"
	"net/http"
	"strconv"
)

// queryPageSize reads a page size or row limit owned by the handler itself.
// Absent, malformed, non-positive, and over-maximum values all answer
// fallback, so a bad limit still returns a page rather than failing the
// request.
//
// The value is parsed at 32 bits and bounded before it is converted, so it can
// size a query or an allocation without wrapping or growing past maximum.
func queryPageSize(request *http.Request, name string, fallback, maximum int32) int32 {
	value, err := strconv.ParseInt(request.URL.Query().Get(name), 10, 32)
	if err != nil || value <= 0 || value > int64(maximum) {
		return fallback
	}
	return int32(value)
}

// parseInt32 reads a whole number for a service that applies its own default
// and bounds, which all read zero or less as "use the default".
//
// Malformed and negative values answer zero. A value beyond the int32 range
// saturates rather than wrapping, so it still lands on the service's maximum
// the way it did when the service received it unconverted.
func parseInt32(raw string) int32 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(value)
}
