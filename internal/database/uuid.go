package database

import (
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
)

func ParseUUIDs(values []string) ([]pgtype.UUID, error) {
	result := make([]pgtype.UUID, 0, len(values))
	for _, value := range values {
		var id pgtype.UUID
		if err := id.Scan(value); err != nil || !id.Valid {
			return nil, errors.New("invalid UUID in list")
		}
		result = append(result, id)
	}
	return result, nil
}

func UUIDStrings(values []pgtype.UUID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !value.Valid {
			continue
		}
		bytes := value.Bytes
		result = append(result, hex(bytes[0:4])+"-"+hex(bytes[4:6])+"-"+hex(bytes[6:8])+"-"+hex(bytes[8:10])+"-"+hex(bytes[10:16]))
	}
	return result
}

func hex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, current := range value {
		result[index*2] = digits[current>>4]
		result[index*2+1] = digits[current&15]
	}
	return string(result)
}
