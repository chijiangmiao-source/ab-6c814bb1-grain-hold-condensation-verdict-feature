package decision

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validInput() Input {
	return Input{Voyage: "V-001", Hatch: "3H", Tg: 25, Ta: 20, RH: 70}
}

func TestEvaluate_AllowedDeniedRetest(t *testing.T) {
	cases := []struct {
		name    string
		tg, ta  float64
		rh      float64
		verdict string
	}{
		{"warm dry grain allows ventilation", 30, 20, 55, VerdictAllowed},
		{"cold damp grain forbids ventilation", 5, 28, 95, VerdictDenied},
		// Td(20 °C, 70 %) ~= 14.36, so a grain temp of 15 sits inside the
		// +/-2 retest band around the dew point.
		{"near equilibrium requires retest", 15, 20, 70, VerdictRetest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			in.Tg, in.Ta, in.RH = tc.tg, tc.ta, tc.rh
			r, err := Evaluate(in)
			require.NoError(t, err)
			assert.Equal(t, tc.verdict, r.Verdict)
		})
	}
}

// The band rule itself: BOTH mathematical endpoints delta = +2.00 and
// delta = -2.00 belong to the retest interval, which is closed on both
// sides. A single ULP beyond each endpoint must decide strictly.
func TestVerdictForDelta_BandEndpointsInclusive(t *testing.T) {
	assert.Equal(t, VerdictRetest, VerdictForDelta(2.0))
	assert.Equal(t, VerdictRetest, VerdictForDelta(-2.0))
	assert.Equal(t, VerdictRetest, VerdictForDelta(0))

	assert.Equal(t, VerdictAllowed, VerdictForDelta(math.Nextafter(2.0, 3.0)))
	assert.Equal(t, VerdictDenied, VerdictForDelta(math.Nextafter(-2.0, -3.0)))

	assert.Equal(t, VerdictAllowed, VerdictForDelta(2.0000001))
	assert.Equal(t, VerdictDenied, VerdictForDelta(-2.0000001))
}

// End-to-end through the Magnus formula: sweep Tg on a fine grid and locate
// the two transitions. The last verdict on the inclusive side must (a) be
// retest, (b) round to exactly +/-2.00 for display, and (c) sit within a
// tiny distance of the mathematical endpoint; the next grid point must
// already decide strictly. This proves neither endpoint leaks into
// allowed/denied through the real computation.
func TestEvaluate_GridHitsBothEndpointsAsRetest(t *testing.T) {
	base := validInput()
	r0, err := Evaluate(base)
	require.NoError(t, err)
	td := r0.Td

	// Grid step 1e-9 °C on Tg; delta has the same step size as Tg.
	const step = 1e-9
	sweep := func(dir float64, wantOutside string) {
		n := 0
		for n < 5_000_000_000 {
			in := base
			in.Tg = td + dir*2.0 - dir*5e-9 + dir*float64(n)*step
			r, err := Evaluate(in)
			require.NoError(t, err)
			if r.Verdict != VerdictRetest {
				assert.Equal(t, wantOutside, r.Verdict, "first point beyond endpoint")
				// Previous point: the inclusive-side neighbor.
				inPrev := base
				inPrev.Tg = td + dir*2.0 - dir*5e-9 + dir*float64(n-1)*step
				prev, err := Evaluate(inPrev)
				require.NoError(t, err)
				require.Equal(t, VerdictRetest, prev.Verdict)

				endpoint := dir * 2.0
				dist := math.Abs(prev.Delta - endpoint)
				assert.Less(t, dist, step, "inclusive side must reach the endpoint")
				assert.Equal(t, endpoint, prev.DeltaDisplay,
					"endpoint displays as the two-decimal boundary value")
				return
			}
			n++
		}
		t.Fatalf("never left the retest band (dir=%v)", dir)
	}

	sweep(+1, VerdictAllowed)
	sweep(-1, VerdictDenied)
}

// Verdict is taken from the UNROUNDED delta. A delta of 2.004 displays as
// 2.00 but still decides "allowed"; -2.004 displays -2.00 but decides
// "denied". The page must therefore render the API verdict, not infer one
// from rounded numbers.
func TestEvaluate_VerdictUsesUnroundedDelta(t *testing.T) {
	base := validInput()
	r0, err := Evaluate(base)
	require.NoError(t, err)

	in := base
	in.Tg = r0.Td + 2.004
	r, err := Evaluate(in)
	require.NoError(t, err)
	assert.Equal(t, VerdictAllowed, r.Verdict)
	assert.Equal(t, 2.00, r.DeltaDisplay)

	in.Tg = r0.Td - 2.004
	r, err = Evaluate(in)
	require.NoError(t, err)
	assert.Equal(t, VerdictDenied, r.Verdict)
	assert.Equal(t, -2.00, r.DeltaDisplay)
}

// Safe distances outside the band decide strictly.
func TestEvaluate_OutsideBand(t *testing.T) {
	base := validInput()
	r0, err := Evaluate(base)
	require.NoError(t, err)

	in := base
	in.Tg = r0.Td + 2.01
	r, err := Evaluate(in)
	require.NoError(t, err)
	assert.Equal(t, VerdictAllowed, r.Verdict)

	in.Tg = r0.Td - 2.01
	r, err = Evaluate(in)
	require.NoError(t, err)
	assert.Equal(t, VerdictDenied, r.Verdict)
}

func TestValidate_RangesAndFiniteness(t *testing.T) {
	badValues := []struct {
		mutate func(*Input)
		field  string
		code   string
	}{
		{func(i *Input) { i.Tg = 60.01 }, "tg", "out_of_range"},
		{func(i *Input) { i.Tg = -20.01 }, "tg", "out_of_range"},
		{func(i *Input) { i.Ta = 60.01 }, "ta", "out_of_range"},
		{func(i *Input) { i.Ta = -20.01 }, "ta", "out_of_range"},
		{func(i *Input) { i.RH = 100.01 }, "rh", "out_of_range"},
		{func(i *Input) { i.RH = 0.99 }, "rh", "out_of_range"},
		{func(i *Input) { i.Tg = math.NaN() }, "tg", "not_finite"},
		{func(i *Input) { i.Ta = math.Inf(1) }, "ta", "not_finite"},
		{func(i *Input) { i.RH = math.Inf(-1) }, "rh", "not_finite"},
		{func(i *Input) { i.Voyage = "" }, "voyage", "required"},
		{func(i *Input) { i.Hatch = "" }, "hatch", "required"},
	}
	for _, bv := range badValues {
		in := validInput()
		bv.mutate(&in)
		err := in.Validate()
		var verr *ValidationError
		require.ErrorAs(t, err, &verr)
		require.NotEmpty(t, verr.Fields)
		assert.Equal(t, bv.field, verr.Fields[0].Field)
		assert.Equal(t, bv.code, verr.Fields[0].Code)
	}

	// Boundary values themselves are legal.
	for _, in := range []Input{
		{Voyage: "v", Hatch: "h", Tg: -20, Ta: 60, RH: 1},
		{Voyage: "v", Hatch: "h", Tg: 60, Ta: -20, RH: 100},
	} {
		assert.NoError(t, in.Validate())
	}
}

// Multiple bad fields are reported together so the form can mark each one.
func TestValidate_CollectsAllFields(t *testing.T) {
	in := Input{Tg: 99, Ta: math.NaN(), RH: 0}
	err := in.Validate()
	var verr *ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Len(t, verr.Fields, 5)
}

// Independent recomputation of a known sample guards the constants and the
// order of operations against accidental edits.
func TestEvaluate_KnownSample(t *testing.T) {
	in := Input{Voyage: "V", Hatch: "H", Tg: 25, Ta: 20, RH: 70}
	r, err := Evaluate(in)
	require.NoError(t, err)

	wantGamma := math.Log(0.7) + 17.62*20/(243.12+20)
	wantTd := 243.12 * wantGamma / (17.62 - wantGamma)
	wantDelta := 25 - wantTd

	assert.InDelta(t, wantGamma, r.Gamma, 1e-12)
	assert.InDelta(t, wantTd, r.Td, 1e-12)
	assert.InDelta(t, wantDelta, r.Delta, 1e-12)
	assert.Equal(t, 14.36, Round2(r.Td))
	assert.Equal(t, 10.64, Round2(r.Delta))
	assert.Equal(t, VerdictAllowed, r.Verdict)
}
