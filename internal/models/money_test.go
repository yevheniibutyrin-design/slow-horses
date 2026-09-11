package models_test

import (
	"encoding/json"
	"testing"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in      string
		want    int64 // minor units
		comment string
	}{
		{"10", 1000, "bare integer"},
		{"10.0", 1000, "one decimal"},
		{"10.00", 1000, "two decimals"},
		{"8.87", 887, "the worked example's entry"},
		{"0.01", 1, "smallest unit"},
		{"0", 0, "zero"},
		{".5", 50, "leading dot"},
		{"-1.25", -125, "negative"},
		{"+1.25", 125, "explicit plus"},
		// A client computing in floating point can emit these for values that are
		// exactly 18.20 and 8.87. Rounding them is a better failure than a 400.
		{"18.200000000000003", 1820, "float artifact rounds back"},
		{"8.8749", 887, "third decimal below 5 rounds down"},
		{"8.8750", 888, "third decimal at 5 rounds half-up"},
		{"0.999", 100, "rounding carries into the units"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := models.ParseAmount(c.in)
			if err != nil {
				t.Fatalf("ParseAmount(%q): %v", c.in, err)
			}
			if int64(got) != c.want {
				t.Errorf("ParseAmount(%q) = %d, want %d (%s)", c.in, int64(got), c.want, c.comment)
			}
		})
	}
}

func TestParseAmountRejects(t *testing.T) {
	for _, in := range []string{"", "  ", "abc", "1.2.3", "1e2", "1E2", "--1", "+", "1,25"} {
		t.Run(in, func(t *testing.T) {
			if _, err := models.ParseAmount(in); err == nil {
				t.Errorf("ParseAmount(%q) accepted a malformed value", in)
			}
		})
	}
}

// The widget's types declare these fields as `number`, so the wire form must be
// an unquoted JSON number with two decimals.
func TestAmountMarshalsAsBareNumberWithTwoDecimals(t *testing.T) {
	type wrapper struct {
		Payout models.Amount `json:"payout"`
	}
	b, err := json.Marshal(wrapper{Payout: 1820})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"payout":18.20}` {
		t.Errorf("marshalled %s, want {\"payout\":18.20}", b)
	}
}

func TestAmountRoundTripsThroughJSON(t *testing.T) {
	for _, raw := range []string{`10`, `10.0`, `8.87`, `18.20`, `0.01`, `"8.87"`, `null`} {
		t.Run(raw, func(t *testing.T) {
			var a models.Amount
			if err := json.Unmarshal([]byte(raw), &a); err != nil {
				t.Fatalf("unmarshal %s: %v", raw, err)
			}
			var again models.Amount
			b, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &again); err != nil {
				t.Fatalf("re-unmarshal %s: %v", b, err)
			}
			if again != a {
				t.Errorf("%s -> %s -> %s lost precision", raw, b, again)
			}
		})
	}
}

// The scale-and-truncate approach this type exists to avoid: 8.87 * 100 is
// 886.9999999999999 in float64 and floors to 8.86.
func TestAmountAvoidsTheFloatTrap(t *testing.T) {
	got, err := models.ParseAmount("8.87")
	if err != nil {
		t.Fatal(err)
	}
	if int64(got) != 887 {
		t.Fatalf("8.87 parsed to %d minor units, want 887", int64(got))
	}

	// The trap this type exists to avoid, exercised rather than asserted about.
	// It goes through a variable on purpose: Go evaluates an untyped constant
	// expression at arbitrary precision, so `int64(8.87*100)` folds to 887 and
	// shows nothing. In float64 at runtime the same arithmetic is
	// 886.9999999999999, and truncating it loses a cent.
	stake := 8.87
	if viaFloat := int64(stake * 100); viaFloat != 886 {
		t.Fatalf("int64(8.87 * 100) in float64 = %d, want 886 — "+
			"if this no longer truncates, the comment above is stale, not the type", viaFloat)
	}
}

func TestAmountString(t *testing.T) {
	cases := map[models.Amount]string{
		0: "0.00", 1: "0.01", 50: "0.50", 887: "8.87", 1820: "18.20", 1887: "18.87",
		100000: "1000.00", -125: "-1.25",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("Amount(%d).String() = %q, want %q", int64(in), got, want)
		}
	}
}
