package customerauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
)

// ErrWidgetPayloadInvalid reports a login-widget document that is not a flat
// JSON object of scalar fields — which is the only shape the widget produces.
var ErrWidgetPayloadInvalid = errors.New("telegram widget payload is not a flat object")

// WidgetValuesFromJSON decodes the JSON document the Telegram Login Widget hands
// to its `data-onauth` callback into the form the signature check takes.
//
// The widget emits `id` and `auth_date` as JSON numbers and everything else as
// strings; Telegram's signing scheme, however, is defined over the textual
// form of every field. Decoding into a map of strings refuses the numbers
// before the signature is ever checked, which is exactly the failure that made
// the widget unusable — and decoding numbers as float64 would turn a large
// Telegram ID into exponent notation and break the signature a different way.
// json.Number keeps the digits Telegram sent, byte for byte.
//
// Booleans are carried as "true"/"false" and null is dropped, for the same
// reason: the value signed is the one the widget serialised. A nested object
// or array is refused outright, because the widget never produces one and a
// caller sending one is not the widget.
func WidgetValuesFromJSON(raw []byte) (url.Values, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		return nil, ErrWidgetPayloadInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrWidgetPayloadInvalid
	}
	return WidgetValuesFromMap(body)
}

// WidgetValuesFromMap is WidgetValuesFromJSON for a document already decoded
// with json.Decoder.UseNumber.
func WidgetValuesFromMap(body map[string]any) (url.Values, error) {
	if body == nil {
		return nil, ErrWidgetPayloadInvalid
	}
	values := url.Values{}
	for key, value := range body {
		switch typed := value.(type) {
		case string:
			values.Set(key, typed)
		case json.Number:
			values.Set(key, typed.String())
		case float64:
			// Only reached by a caller that decoded without UseNumber. The
			// integer form is the one Telegram signed.
			values.Set(key, strconv.FormatFloat(typed, 'f', -1, 64))
		case bool:
			values.Set(key, strconv.FormatBool(typed))
		case nil:
			// An absent field is not part of the signed string.
		default:
			return nil, ErrWidgetPayloadInvalid
		}
	}
	return values, nil
}
