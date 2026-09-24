// Package money implements an exact monetary value object backed by int64
// minor units. Money never passes through floating point.
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// Currency is an ISO 4217 code. Every supported currency has scale 2.
type Currency string

const (
	BRL Currency = "BRL"
	USD Currency = "USD"
	EUR Currency = "EUR"
)

const scale = 100

var (
	ErrInvalidAmount       = errors.New("money: invalid amount")
	ErrUnsupportedCurrency = errors.New("money: unsupported currency")
	ErrCurrencyMismatch    = errors.New("money: currency mismatch")
	ErrOverflow            = errors.New("money: overflow")
	ErrZeroValue           = errors.New("money: uninitialized value")
)

// canonical is the only accepted external form: non-negative, no leading
// zeros, exactly two decimals.
var canonical = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{2}$`)

func ParseCurrency(s string) (Currency, error) {
	switch c := Currency(s); c {
	case BRL, USD, EUR:
		return c, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnsupportedCurrency, s)
}

// Money is immutable. Its zero value is invalid and rejected by every operation.
type Money struct {
	minor    int64
	currency Currency
}

// Parse builds Money from the canonical external decimal form.
func Parse(amount, currency string) (Money, error) {
	c, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	if !canonical.MatchString(amount) {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidAmount, amount)
	}
	units, err := strconv.ParseInt(amount[:len(amount)-3], 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	cents, err := strconv.ParseInt(amount[len(amount)-2:], 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidAmount, amount)
	}
	if units > (math.MaxInt64-cents)/scale {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	return Money{minor: units*scale + cents, currency: c}, nil
}

// FromMinor builds Money from minor units. Negative values are allowed for
// internal calculations; external input goes through Parse.
func FromMinor(minor int64, c Currency) (Money, error) {
	if _, err := ParseCurrency(string(c)); err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: c}, nil
}

func Zero(c Currency) (Money, error) { return FromMinor(0, c) }

func (m Money) Minor() int64       { return m.minor }
func (m Money) Currency() Currency { return m.currency }
func (m Money) Valid() bool        { return m.currency != "" }
func (m Money) IsZero() bool       { return m.minor == 0 }
func (m Money) IsPositive() bool   { return m.minor > 0 }
func (m Money) IsNegative() bool   { return m.minor < 0 }

func (m Money) Add(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.minor > 0 && m.minor > math.MaxInt64-o.minor) || (o.minor < 0 && m.minor < math.MinInt64-o.minor) {
		return Money{}, fmt.Errorf("%w: %s + %s", ErrOverflow, m, o)
	}
	return Money{minor: m.minor + o.minor, currency: m.currency}, nil
}

func (m Money) Sub(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.minor < 0 && m.minor > math.MaxInt64+o.minor) || (o.minor > 0 && m.minor < math.MinInt64+o.minor) {
		return Money{}, fmt.Errorf("%w: %s - %s", ErrOverflow, m, o)
	}
	return Money{minor: m.minor - o.minor, currency: m.currency}, nil
}

func (m Money) Neg() (Money, error) {
	if !m.Valid() {
		return Money{}, ErrZeroValue
	}
	if m.minor == math.MinInt64 {
		return Money{}, fmt.Errorf("%w: -(%s)", ErrOverflow, m)
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

// Cmp returns -1, 0 or 1 comparing m to o.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.compatible(o); err != nil {
		return 0, err
	}
	switch {
	case m.minor < o.minor:
		return -1, nil
	case m.minor > o.minor:
		return 1, nil
	}
	return 0, nil
}

// String renders the canonical decimal form, with a leading minus when negative.
func (m Money) String() string {
	sign := ""
	u := uint64(m.minor)
	if m.minor < 0 {
		sign = "-"
		u = uint64(-(m.minor + 1)) + 1
	}
	return fmt.Sprintf("%s%d.%02d", sign, u/scale, u%scale)
}

func (m Money) MarshalJSON() ([]byte, error) {
	if !m.Valid() {
		return nil, ErrZeroValue
	}
	return json.Marshal(struct {
		Amount   string   `json:"amount"`
		Currency Currency `json:"currency"`
	}{m.String(), m.currency})
}

func (m Money) compatible(o Money) error {
	if !m.Valid() || !o.Valid() {
		return ErrZeroValue
	}
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}
