package wagering

import (
	"errors"
	"testing"
)

func TestParseExternalKind(t *testing.T) {
	for _, s := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		if k, err := ParseExternalKind(s); err != nil || string(k) != s {
			t.Errorf("ParseExternalKind(%q) = %q, %v", s, k, err)
		}
	}
	if _, err := ParseExternalKind("OPENING"); !errors.Is(err, ErrOpeningNotExternal) {
		t.Errorf("OPENING error = %v", err)
	}
	for _, s := range []string{"", "bet", "DEPOSIT"} {
		if _, err := ParseExternalKind(s); !errors.Is(err, ErrUnknownKind) {
			t.Errorf("ParseExternalKind(%q) error = %v", s, err)
		}
	}
}
