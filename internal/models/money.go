package models

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Amount is a money value in minor units (cents), so no stake, payout or pot is
// ever held as a float. `docs/bet-room-api.md` §3 records why: the duel entry is
// derived by dividing and flooring, and the obvious float approach is wrong —
// 8.87 * 100 is 886.9999999999999 and floors to 8.86.
//
// It crosses the wire as a JSON number with two decimals and is stored in MongoDB
// as an int64, because the underlying type is int64 and the driver marshals named
// integer types natively.
type Amount int64

// minorUnits is the scale: 100 cents to the unit, i.e. two decimal places.
const minorUnits = 100

// ParseAmount reads a decimal literal such as "8.87" into minor units, exactly.
// More than two decimal places are rounded half-up rather than rejected: a client
// computing in floating point can emit 18.200000000000003 for a value that is
// 18.20, and refusing that would be a worse failure than rounding it.
func ParseAmount(s string) (Amount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty amount")
	}

	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return 0, errors.New("malformed amount")
	}
	if strings.ContainsAny(s, "eE") {
		// Exponent form is never produced for money-sized numbers and would need
		// float parsing to read, which is the one thing this type exists to avoid.
		return 0, errors.New("exponent notation is not accepted for money values")
	}

	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if intPart == "" {
		intPart = "0"
	}
	if !isDigits(intPart) || (hasFrac && !isDigits(fracPart)) {
		return 0, errors.New("malformed amount")
	}

	units, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, errors.New("amount out of range")
	}

	// Pad or round the fraction to exactly two digits, half-up on the third.
	var cents int64
	switch {
	case len(fracPart) == 0:
		cents = 0
	case len(fracPart) == 1:
		cents = int64(fracPart[0]-'0') * 10
	default:
		cents = int64(fracPart[0]-'0')*10 + int64(fracPart[1]-'0')
		if len(fracPart) > 2 && fracPart[2] >= '5' {
			cents++
		}
	}
	if cents >= minorUnits { // a carry out of .99 + rounding
		cents -= minorUnits
		units++
	}

	total := units*minorUnits + cents
	if neg {
		total = -total
	}
	return Amount(total), nil
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// String renders the amount with two decimals, e.g. "18.20".
func (a Amount) String() string {
	neg := a < 0
	v := int64(a)
	if neg {
		v = -v
	}
	s := strconv.FormatInt(v/minorUnits, 10) + "." + twoDigits(v%minorUnits)
	if neg {
		return "-" + s
	}
	return s
}

func twoDigits(v int64) string {
	if v < 10 {
		return "0" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}

// MarshalJSON emits a bare JSON number with two decimals. The widget's types
// declare these fields as `number`, so they must not be quoted.
func (a Amount) MarshalJSON() ([]byte, error) {
	return []byte(a.String()), nil
}

// UnmarshalJSON accepts a JSON number, and a quoted decimal for tolerance.
func (a *Amount) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*a = 0
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var quoted string
		if err := json.Unmarshal(b, &quoted); err != nil {
			return err
		}
		s = quoted
	}
	parsed, err := ParseAmount(s)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}
