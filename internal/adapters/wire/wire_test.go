package wire

import (
	"errors"
	"testing"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

func TestParseUUIDAcceptsOnlyCanonicalLowercase(t *testing.T) {
	ok := "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	if got, err := ParseUUID("playerId", ok); err != nil || got != ok {
		t.Fatalf("ParseUUID = %q, %v", got, err)
	}
	for _, bad := range []string{"", "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1", "{0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1}", "urn:uuid:0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", "not-a-uuid"} {
		if _, err := ParseUUID("playerId", bad); !errors.Is(err, app.ErrInvalidInput) {
			t.Errorf("ParseUUID(%q) error = %v", bad, err)
		}
	}
}

func TestMoneyParse(t *testing.T) {
	m, err := Money{Amount: "25.00", Currency: "BRL"}.Parse("money")
	if err != nil || m.String() != "25.00" {
		t.Fatalf("Parse = %v, %v", m, err)
	}
	if _, err := (Money{Amount: "25", Currency: "BRL"}).Parse("money"); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("non canonical error = %v", err)
	}
}
