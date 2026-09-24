package money

import (
	"errors"
	"math"
	"testing"
)

func TestParseAcceptsCanonicalAmounts(t *testing.T) {
	cases := map[string]int64{
		"0.00":                 0,
		"0.01":                 1,
		"25.00":                2500,
		"1000.00":              100000,
		"92233720368547758.07": math.MaxInt64,
	}
	for in, want := range cases {
		m, err := Parse(in, "BRL")
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if m.Minor() != want || m.Currency() != BRL {
			t.Fatalf("Parse(%q) = %d %s, want %d BRL", in, m.Minor(), m.Currency(), want)
		}
		if m.String() != in {
			t.Fatalf("String() = %q, want %q", m.String(), in)
		}
	}
}

func TestParseRejectsNonCanonicalInput(t *testing.T) {
	for _, in := range []string{
		"", "25", "25.0", "25.000", "-1.00", "+1.00", "01.00", "00.00", " 1.00", "1.00 ",
		"1e2", "1E2", "NaN", "Infinity", "-Infinity", "1,00", ".50", "1.", "0x10.00",
	} {
		if _, err := Parse(in, "BRL"); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalidAmount", in, err)
		}
	}
}

func TestParseRejectsOverflow(t *testing.T) {
	for _, in := range []string{"92233720368547758.08", "99999999999999999999.00"} {
		if _, err := Parse(in, "BRL"); !errors.Is(err, ErrOverflow) {
			t.Errorf("Parse(%q) error = %v, want ErrOverflow", in, err)
		}
	}
}

func TestParseRejectsUnsupportedCurrency(t *testing.T) {
	for _, c := range []string{"", "brl", "XYZ", "BR", "BRLL"} {
		if _, err := Parse("1.00", c); !errors.Is(err, ErrUnsupportedCurrency) {
			t.Errorf("Parse currency %q error = %v, want ErrUnsupportedCurrency", c, err)
		}
	}
}

func TestArithmetic(t *testing.T) {
	a := mustParse(t, "100.00", "BRL")
	b := mustParse(t, "80.00", "BRL")

	sum, err := a.Add(b)
	if err != nil || sum.String() != "180.00" {
		t.Fatalf("Add = %v, %v", sum, err)
	}
	diff, err := b.Sub(a)
	if err != nil || diff.String() != "-20.00" || !diff.IsNegative() {
		t.Fatalf("Sub = %v, %v", diff, err)
	}
	neg, err := a.Neg()
	if err != nil || neg.Minor() != -10000 {
		t.Fatalf("Neg = %v, %v", neg, err)
	}
	if c, err := a.Cmp(b); err != nil || c != 1 {
		t.Fatalf("Cmp = %d, %v", c, err)
	}
	if c, _ := b.Cmp(a); c != -1 {
		t.Fatalf("Cmp reversed = %d", c)
	}
	if c, _ := a.Cmp(a); c != 0 {
		t.Fatalf("Cmp equal = %d", c)
	}
}

func TestArithmeticRejectsCurrencyMismatch(t *testing.T) {
	brl := mustParse(t, "1.00", "BRL")
	usd := mustParse(t, "1.00", "USD")
	if _, err := brl.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Add error = %v", err)
	}
	if _, err := brl.Sub(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Sub error = %v", err)
	}
	if _, err := brl.Cmp(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Cmp error = %v", err)
	}
}

func TestArithmeticDetectsOverflow(t *testing.T) {
	maxM, _ := FromMinor(math.MaxInt64, BRL)
	minM, _ := FromMinor(math.MinInt64, BRL)
	one, _ := FromMinor(1, BRL)

	if _, err := maxM.Add(one); !errors.Is(err, ErrOverflow) {
		t.Errorf("max+1 error = %v", err)
	}
	if _, err := minM.Sub(one); !errors.Is(err, ErrOverflow) {
		t.Errorf("min-1 error = %v", err)
	}
	if _, err := minM.Neg(); !errors.Is(err, ErrOverflow) {
		t.Errorf("-min error = %v", err)
	}
	negOne, _ := FromMinor(-1, BRL)
	if _, err := maxM.Sub(negOne); !errors.Is(err, ErrOverflow) {
		t.Errorf("max-(-1) error = %v", err)
	}
}

func TestZeroValueIsRejected(t *testing.T) {
	var zero Money
	one := mustParse(t, "1.00", "BRL")
	if zero.Valid() {
		t.Fatal("zero value must be invalid")
	}
	if _, err := zero.Add(one); !errors.Is(err, ErrZeroValue) {
		t.Errorf("Add error = %v", err)
	}
	if _, err := zero.MarshalJSON(); !errors.Is(err, ErrZeroValue) {
		t.Errorf("MarshalJSON error = %v", err)
	}
	if _, err := FromMinor(1, Currency("XYZ")); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Errorf("FromMinor error = %v", err)
	}
}

func TestStringAndJSON(t *testing.T) {
	minM, _ := FromMinor(math.MinInt64, BRL)
	if got := minM.String(); got != "-92233720368547758.08" {
		t.Fatalf("String(min) = %q", got)
	}
	small, _ := FromMinor(-1, BRL)
	if got := small.String(); got != "-0.01" {
		t.Fatalf("String(-1) = %q", got)
	}
	b, err := mustParse(t, "25.00", "BRL").MarshalJSON()
	if err != nil || string(b) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("MarshalJSON = %s, %v", b, err)
	}
}

func TestZero(t *testing.T) {
	z, err := Zero(BRL)
	if err != nil || !z.IsZero() || !z.Valid() || z.String() != "0.00" {
		t.Fatalf("Zero = %v, %v", z, err)
	}
}

func mustParse(t *testing.T, amount, currency string) Money {
	t.Helper()
	m, err := Parse(amount, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
