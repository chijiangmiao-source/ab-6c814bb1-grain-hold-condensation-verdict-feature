// Package decision implements the dew-point ventilation rule for bulk
// grain carriers. It is the single source of truth for the math: both the
// HTTP handlers and the tests call Evaluate, so the page and API can never
// disagree.
package decision

import (
	"fmt"
	"math"
)

// Boundaries for a single ventilation assessment.
const (
	MinTempC = -20.0
	MaxTempC = 60.0
	MinRH    = 1.0
	MaxRH    = 100.0

	// Magnus coefficients (Alduchov & Eskridge form used in the task).
	magnusA = 17.62
	magnusB = 243.12

	// GreenBand is the half-width of the retest zone around delta = 0.
	GreenBand = 2.0
)

// Verdict values stored and returned by the API.
const (
	VerdictAllowed = "allowed" // Δ > 2.00
	VerdictDenied  = "denied"  // Δ < -2.00
	VerdictRetest  = "retest"  // -2.00 <= Δ <= 2.00 (both endpoints included)
)

// Input is one submitted assessment form.
type Input struct {
	Voyage string  `json:"voyage"`
	Hatch  string  `json:"hatch"`
	Tg     float64 `json:"tg"` // grain temperature, °C
	Ta     float64 `json:"ta"` // hold air temperature, °C
	RH     float64 `json:"rh"` // relative humidity, %
}

// Result holds every intermediate value at full float64 precision plus the
// rounded display values. Store the unrounded ones; show the rounded ones.
type Result struct {
	Gamma        float64 `json:"gamma"`         // γ = ln(RH/100) + a·Ta/(b+Ta)
	Td           float64 `json:"td"`            // dew point = b·γ/(a-γ)
	Delta        float64 `json:"delta"`         // Δ = Tg - Td, unrounded
	Verdict      string  `json:"verdict"`       // allowed | denied | retest
	GammaDisplay float64 `json:"gamma_display"` // rounded for display
	TdDisplay    float64 `json:"td_display"`    // rounded for display
	DeltaDisplay float64 `json:"delta_display"` // rounded for display
}

// FieldError describes one rejected field. Code is machine readable,
// Message is a human readable Chinese string rendered under the field.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationError collects all invalid fields.
type ValidationError struct {
	Fields []FieldError `json:"fields"`
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed on %d field(s)", len(e.Fields))
}

// Validate checks finiteness and ranges. Every non-finite or out-of-range
// numeric value is reported individually and the caller must reject the
// whole request (422) without persisting anything.
func (in Input) Validate() error {
	var errs []FieldError

	checkNumber := func(field string, v, lo, hi float64, label string) {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			errs = append(errs, FieldError{field, "not_finite", label + "必须为有限数值"})
			return
		}
		if v < lo || v > hi {
			errs = append(errs, FieldError{field, "out_of_range",
				fmt.Sprintf("%s必须在 %.1f 至 %.1f 之间", label, lo, hi)})
		}
	}

	checkNumber("tg", in.Tg, MinTempC, MaxTempC, "粮温 Tg")
	checkNumber("ta", in.Ta, MinTempC, MaxTempC, "舱内气温 Ta")
	checkNumber("rh", in.RH, MinRH, MaxRH, "相对湿度 RH")

	if in.Voyage == "" {
		errs = append(errs, FieldError{"voyage", "required", "航次代号不能为空"})
	}
	if in.Hatch == "" {
		errs = append(errs, FieldError{"hatch", "required", "舱号不能为空"})
	}

	if len(errs) > 0 {
		return &ValidationError{Fields: errs}
	}
	return nil
}

// Evaluate runs the Magnus dew-point formula. The verdict is always taken
// from the UNROUNDED delta; rounding happens only for the display fields.
func Evaluate(in Input) (*Result, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	gamma := math.Log(in.RH/100.0) + magnusA*in.Ta/(magnusB+in.Ta)
	td := magnusB * gamma / (magnusA - gamma)
	delta := in.Tg - td

	return &Result{
		Gamma:        gamma,
		Td:           td,
		Delta:        delta,
		Verdict:      VerdictForDelta(delta),
		GammaDisplay: Round2(gamma),
		TdDisplay:    Round2(td),
		DeltaDisplay: Round2(delta),
	}, nil
}

// VerdictForDelta applies the band rule to an unrounded delta: delta > 2
// allowed, delta < -2 denied, the closed interval [-2, 2] inclusive retest.
func VerdictForDelta(delta float64) string {
	switch {
	case delta > GreenBand:
		return VerdictAllowed
	case delta < -GreenBand:
		return VerdictDenied
	default:
		return VerdictRetest
	}
}

// Round2 rounds half away from zero to two decimals (spreadsheet-style
// 四舍五入). math.Round already handles negative values correctly.
func Round2(v float64) float64 {
	return math.Round(v*100) / 100
}
