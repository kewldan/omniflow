package importservice

import (
	"errors"
	"strings"
	"testing"
)

func TestParseFileReadsColumnsByName(t *testing.T) {
	rows, invalid, err := ParseFile(strings.NewReader(
		"\ufeffusername,telegram_id,remnawave_id,status\n" +
			"alice,1001,17,active\n" +
			"bob,,18,\n" +
			"broken,abc,19,\n" +
			"nobody,1004,not-a-number,\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RemnawaveID != 17 || *rows[0].TelegramID != 1001 || rows[0].Status != "ACTIVE" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[1].TelegramID != nil || rows[1].Username != "bob" {
		t.Fatalf("a row without a Telegram ID keeps the rest: %+v", rows[1])
	}
	if len(invalid) != 2 || invalid[0] != 4 || invalid[1] != 5 {
		t.Fatalf("invalid lines = %v", invalid)
	}
}

func TestParseFileNeedsTheRemnawaveColumn(t *testing.T) {
	if _, _, err := ParseFile(strings.NewReader("telegram_id\n1001\n")); !errors.Is(err, ErrFileFormat) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := ParseFile(strings.NewReader("")); !errors.Is(err, ErrFileFormat) {
		t.Fatalf("empty file err = %v", err)
	}
}
