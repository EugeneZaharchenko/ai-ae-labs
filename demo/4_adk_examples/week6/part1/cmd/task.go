package main

// The work the maker is asked to do: one function from LEDGERWORKS' billing
// service, the refund proration whose float64 rounding lost a cent on large
// amounts (bug #423 in the legend).
//
// The visible/holdout split is the point of the task. The loop's gate runs the
// VISIBLE tests, so the maker (and its critics) can see what is being asked. The
// HOLDOUT tests are run once, after the loop has stopped. A loop graded only by
// the tests that steer it measures its ability to satisfy those tests, not its
// ability to write the function — and the buggy variant below is green on the
// visible file and red on the holdout, which is exactly the shape of bug #423.

const taskPrompt = `Write a Go function for the LEDGERWORKS billing service
(package billing, one file, standard library only):

  var ErrInvalid = errors.New("invalid input")
  func ProrateRefund(amountCents int64, daysUsed, periodDays int) (int64, error)

It returns the refund for the unused part of a billing period:
amountCents * (periodDays-daysUsed) / periodDays, rounded to a whole cent half-up
(0.5 cent rounds up). It returns an error wrapping ErrInvalid when periodDays <= 0,
daysUsed < 0, daysUsed > periodDays, or amountCents < 0.
The amount can be very large: the result must not depend on int64 overflow or on
float64 precision.`

// visibleTests is the specification the gate runs inside the loop.
const visibleTests = `package billing

import (
	"errors"
	"testing"
)

func TestProrateRefund(t *testing.T) {
	tests := []struct {
		amount    int64
		used, per int
		want      int64
	}{
		{1000, 10, 30, 667},
		{1000, 0, 30, 1000},
		{1000, 30, 30, 0},
		{100, 1, 3, 67},
		{1, 1, 2, 1},
		{5, 1, 4, 4},
	}
	for _, tt := range tests {
		got, err := ProrateRefund(tt.amount, tt.used, tt.per)
		if err != nil || got != tt.want {
			t.Errorf("ProrateRefund(%d,%d,%d) = %d, %v; want %d", tt.amount, tt.used, tt.per, got, err, tt.want)
		}
	}
}

func TestProrateRefundInvalid(t *testing.T) {
	for _, c := range [][3]int64{{100, 1, 0}, {100, -1, 30}, {100, 31, 30}, {-5, 1, 30}} {
		if _, err := ProrateRefund(c[0], int(c[1]), int(c[2])); !errors.Is(err, ErrInvalid) {
			t.Errorf("ProrateRefund%v: err = %v, want ErrInvalid", c, err)
		}
	}
}
`

// holdoutTests is run only after the loop. The maker never sees it.
const holdoutTests = `package billing

import "testing"

func TestProrateRefundLargeAmounts(t *testing.T) {
	tests := []struct {
		amount    int64
		used, per int
		want      int64
	}{
		{999999999999999999, 0, 30, 999999999999999999},
		{9223372036854775807, 15, 30, 4611686018427387904},
		{9007199254740993, 1, 2, 4503599627370497},
	}
	for _, tt := range tests {
		got, err := ProrateRefund(tt.amount, tt.used, tt.per)
		if err != nil || got != tt.want {
			t.Errorf("ProrateRefund(%d,%d,%d) = %d, %v; want %d", tt.amount, tt.used, tt.per, got, err, tt.want)
		}
	}
}
`

// buggyCandidate is what a hurried maker writes: correct on every case the
// visible file lists, wrong on large amounts, because float64 has 53 bits of
// mantissa. The offline stand-in emits it first.
const buggyCandidate = `package billing

import (
	"errors"
	"math"
)

var ErrInvalid = errors.New("invalid input")

func ProrateRefund(amountCents int64, daysUsed, periodDays int) (int64, error) {
	if periodDays <= 0 || daysUsed < 0 || daysUsed > periodDays || amountCents < 0 {
		return 0, ErrInvalid
	}
	r := float64(amountCents) * float64(periodDays-daysUsed) / float64(periodDays)
	return int64(math.Floor(r + 0.5)), nil
}
`

// referenceCandidate does the arithmetic in math/big and rounds half-up as
// (2*num + den) / (2*den). It passes both files.
const referenceCandidate = `package billing

import (
	"errors"
	"math/big"
)

var ErrInvalid = errors.New("invalid input")

func ProrateRefund(amountCents int64, daysUsed, periodDays int) (int64, error) {
	if periodDays <= 0 || daysUsed < 0 || daysUsed > periodDays || amountCents < 0 {
		return 0, ErrInvalid
	}
	num := new(big.Int).Mul(big.NewInt(amountCents), big.NewInt(int64(periodDays-daysUsed)))
	den := big.NewInt(int64(periodDays))
	num.Lsh(num, 1)
	num.Add(num, den)
	den.Lsh(den, 1)
	return num.Quo(num, den).Int64(), nil
}
`
