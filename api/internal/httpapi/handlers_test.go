package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"grain-ventilation/internal/decision"
	"grain-ventilation/internal/store"
)

func setup(t *testing.T) *gin.Engine {
	t.Helper()
	_, r := setupWithStore(t)
	return r
}

func setupWithStore(t *testing.T) (*store.Store, *gin.Engine) {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	return st, NewRouter(st)
}

func do(t *testing.T, r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func doRaw(t *testing.T, r http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/assessments", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func validBody() map[string]any {
	return map[string]any{"voyage": "V-100", "hatch": "4H", "tg": 25.0, "ta": 20.0, "rh": 70.0}
}

func postMap(t *testing.T, r http.Handler, body map[string]any) (int, map[string]any) {
	t.Helper()
	w := do(t, r, http.MethodPost, "/api/assessments", body)
	var got map[string]any
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	}
	return w.Code, got
}

func getMap(t *testing.T, r http.Handler, path string) (int, map[string]any) {
	t.Helper()
	w := do(t, r, http.MethodGet, path, nil)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	return w.Code, got
}

// Interleaved submissions across voyages/hatches prove the predecessor link
// is scoped by voyage AND hatch and follows creation order.
func TestCreate_PredecessorLinkingInterleaved(t *testing.T) {
	r := setup(t)

	submit := func(voyage, hatch string, tg float64) map[string]any {
		body := validBody()
		body["voyage"], body["hatch"], body["tg"] = voyage, hatch, tg
		w, got := postMap(t, r, body)
		require.Equal(t, http.StatusCreated, w, got)
		return got
	}
	detail := func(id float64) map[string]any {
		w, got := getMap(t, r, "/api/assessments/"+strconv.FormatFloat(id, 'f', 0, 64))
		require.Equal(t, http.StatusOK, w)
		return got
	}

	a1 := submit("V-100", "4H", 25)
	b1 := submit("V-100", "5H", 25)
	c1 := submit("V-200", "4H", 25) // same hatch number, different voyage
	a2 := submit("V-100", "4H", 24)
	b2 := submit("V-100", "5H", 23)
	c2 := submit("V-200", "4H", 22)
	a3 := submit("V-100", "4H", 26)

	// First measurements carry no comparison block at all.
	assert.NotContains(t, detail(a1["id"].(float64)), "comparison")

	expectPrev := func(cur, want map[string]any) {
		t.Helper()
		d := detail(cur["id"].(float64))
		cmp, ok := d["comparison"].(map[string]any)
		require.True(t, ok, "detail of a repeat measurement carries a comparison")
		assert.Equal(t, true, cmp["available"])
		prev := cmp["previous"].(map[string]any)
		assert.Equal(t, want["id"], prev["id"])
		assert.Equal(t, "V-100", prev["voyage"])
	}
	expectPrev(a2, a1)
	expectPrev(b2, b1)

	// The V-200 chain must never be linked to the identically-numbered V-100 hatch.
	c2d := detail(c2["id"].(float64))
	c2prev := c2d["comparison"].(map[string]any)["previous"].(map[string]any)
	assert.Equal(t, c1["id"], c2prev["id"])
	assert.Equal(t, "V-200", c2prev["voyage"])

	// The chain follows creation order, not global recency.
	expectPrev(a3, a2)

	// Tg changed -1 with identical Ta/RH: unrounded Δ and Tg changes are
	// exactly -1 and 0 respectively, and every change key is present.
	a2cmp := detail(a2["id"].(float64))["comparison"].(map[string]any)
	changes := a2cmp["changes"].(map[string]any)
	for _, k := range []string{"tg", "ta", "rh", "td", "delta"} {
		_, ok := changes[k]
		assert.True(t, ok, "comparison.changes carries %s", k)
	}
	assert.Equal(t, -1.0, changes["tg"])
	assert.Equal(t, 0.0, changes["ta"])
	assert.Equal(t, 0.0, changes["rh"])
	assert.Equal(t, 0.0, changes["td"])
	assert.Equal(t, -1.0, changes["delta"])

	// The comparison block exists ONLY on detail responses: the POST body
	// and list items keep their original fields.
	assert.NotContains(t, a2, "comparison")
	w, list := getMap(t, r, "/api/assessments")
	require.Equal(t, http.StatusOK, w)
	for _, it := range list["items"].([]any) {
		assert.NotContains(t, it.(map[string]any), "comparison")
	}
}

// Change quantities must be the SERVER's unrounded subtraction, never a
// rounded display delta and never something the browser could re-derive.
func TestGet_ComparisonChangesAreUnrounded(t *testing.T) {
	r := setup(t)

	// Independent expected values from the decision package.
	in1 := decision.Input{Voyage: "V-D", Hatch: "1P", Tg: 25.345, Ta: 20.123, RH: 71.5}
	in2 := decision.Input{Voyage: "V-D", Hatch: "1P", Tg: 24.117, Ta: 21.987, RH: 68.25}
	body1 := map[string]any{"voyage": "V-D", "hatch": "1P", "tg": in1.Tg, "ta": in1.Ta, "rh": in1.RH}
	body2 := map[string]any{"voyage": "V-D", "hatch": "1P", "tg": in2.Tg, "ta": in2.Ta, "rh": in2.RH}

	w, first := postMap(t, r, body1)
	require.Equal(t, http.StatusCreated, w)
	w, second := postMap(t, r, body2)
	require.Equal(t, http.StatusCreated, w)

	w, d := getMap(t, r, "/api/assessments/"+strconv.FormatFloat(second["id"].(float64), 'f', 0, 64))
	require.Equal(t, http.StatusOK, w)
	cmp := d["comparison"].(map[string]any)
	assert.Equal(t, true, cmp["available"])
	assert.Equal(t, first["id"], cmp["previous"].(map[string]any)["id"])

	r1, err := decision.Evaluate(in1)
	require.NoError(t, err)
	r2, err := decision.Evaluate(in2)
	require.NoError(t, err)

	changes := cmp["changes"].(map[string]any)
	assert.Equal(t, in2.Tg-in1.Tg, changes["tg"])
	assert.Equal(t, in2.Ta-in1.Ta, changes["ta"])
	assert.Equal(t, in2.RH-in1.RH, changes["rh"])
	assert.Equal(t, r2.Td-r1.Td, changes["td"])
	assert.Equal(t, r2.Delta-r1.Delta, changes["delta"])

	// Pin that these are unrounded values: at least the delta change is not
	// its own 2-dp rounding.
	rawDeltaChange := r2.Delta - r1.Delta
	assert.NotEqual(t, decision.Round2(rawDeltaChange), rawDeltaChange)
	assert.Equal(t, rawDeltaChange, changes["delta"])

	// The predecessor summary carries the previous verdict and persisted values.
	prev := cmp["previous"].(map[string]any)
	assert.Equal(t, r1.Verdict, prev["verdict"])
	assert.Equal(t, r1.Td, prev["td"])
	assert.Equal(t, r1.Delta, prev["delta"])
}

// A 422 between two valid measurements must not become a predecessor link.
func TestCreate_RejectedSubmissionDoesNotEnterChain(t *testing.T) {
	r := setup(t)
	first := validBody()
	w, a1 := postMap(t, r, first)
	require.Equal(t, http.StatusCreated, w)

	bad := validBody()
	bad["tg"] = 999
	bad["voyage"], bad["hatch"] = "V-100", "4H"
	require.Equal(t, http.StatusUnprocessableEntity, do(t, r, http.MethodPost, "/api/assessments", bad).Code)

	second := validBody()
	second["tg"] = 24.0
	w, a2 := postMap(t, r, second)
	require.Equal(t, http.StatusCreated, w)

	w, d := getMap(t, r, "/api/assessments/"+strconv.FormatFloat(a2["id"].(float64), 'f', 0, 64))
	require.Equal(t, http.StatusOK, w)
	cmp := d["comparison"].(map[string]any)
	assert.Equal(t, true, cmp["available"])
	assert.Equal(t, a1["id"], cmp["previous"].(map[string]any)["id"])
}

// Boundary: the saved predecessor vanished. The current assessment is still
// returned normally with the comparison explicitly marked unavailable.
func TestGet_ComparisonUnavailableWhenPredecessorMissing(t *testing.T) {
	st, r := setupWithStore(t)

	w, first := postMap(t, r, validBody())
	require.Equal(t, http.StatusCreated, w)
	secondBody := validBody()
	secondBody["tg"] = 24.0
	w, second := postMap(t, r, secondBody)
	require.Equal(t, http.StatusCreated, w)

	// Real traffic can never produce a dangling link; simulate one through
	// the test seam (as if the predecessor row had been deleted).
	require.NoError(t, st.SetPrevIDForTest(context.Background(),
		int64(second["id"].(float64)), int64(first["id"].(float64))+9999))

	w, d := getMap(t, r, "/api/assessments/"+strconv.FormatFloat(second["id"].(float64), 'f', 0, 64))
	require.Equal(t, http.StatusOK, w, "current assessment is still returned")
	assert.Equal(t, "allowed", d["verdict"])
	assert.Equal(t, second["delta"], d["delta"])
	cmp := d["comparison"].(map[string]any)
	assert.Equal(t, false, cmp["available"])
	assert.NotContains(t, cmp, "previous")
	assert.NotContains(t, cmp, "changes")
	assert.Contains(t, cmp["reason"], "已不存在")
	assert.Equal(t, first["id"].(float64)+9999, cmp["prev_id"])
}

// Boundary: the saved predecessor exists but belongs to another voyage/hatch.
// The comparison is marked unavailable and the API must not silently rebind
// to some other same-hatch record.
func TestGet_ComparisonUnavailableWhenPredecessorOtherChain(t *testing.T) {
	st, r := setupWithStore(t)

	w, other := postMap(t, r, map[string]any{"voyage": "V-OTHER", "hatch": "9H", "tg": 25, "ta": 20, "rh": 70})
	require.Equal(t, http.StatusCreated, w)
	// A genuine same-chain row exists too: it must never be rebound after
	// the saved link is found to point at the other voyage/hatch.
	w, _ = postMap(t, r, validBody())
	require.Equal(t, http.StatusCreated, w)
	secondBody := validBody()
	secondBody["tg"] = 24.0
	w, second := postMap(t, r, secondBody)
	require.Equal(t, http.StatusCreated, w)

	require.NoError(t, st.SetPrevIDForTest(context.Background(),
		int64(second["id"].(float64)), int64(other["id"].(float64))))

	w, d := getMap(t, r, "/api/assessments/"+strconv.FormatFloat(second["id"].(float64), 'f', 0, 64))
	require.Equal(t, http.StatusOK, w)
	assert.Equal(t, "allowed", d["verdict"])
	cmp := d["comparison"].(map[string]any)
	assert.Equal(t, false, cmp["available"])
	assert.Contains(t, cmp["reason"], "同一航次同一舱")
	assert.Contains(t, cmp["reason"], "未临时改绑")
	assert.NotContains(t, cmp, "previous")
	assert.Equal(t, other["id"], cmp["prev_id"])
}

func TestCreate_SuccessAndFormula(t *testing.T) {
	r := setup(t)
	w := do(t, r, http.MethodPost, "/api/assessments", validBody())
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.Equal(t, "allowed", created["verdict"])
	assert.Equal(t, 14.36, created["td_display"])
	assert.Equal(t, 10.64, created["delta_display"])

	formula := created["formula"].(map[string]any)
	for _, key := range []string{"gamma_line", "td_line", "delta_line", "rule_line"} {
		assert.NotEmpty(t, formula[key])
	}
	assert.Contains(t, formula["delta_line"], "25 −")
	// Unrounded values are present at a different precision than display.
	assert.NotEqual(t, created["delta"], created["delta_display"])
}

// A 422 of any flavour must never produce a row.
func TestCreate_InvalidLeavesNoRecord(t *testing.T) {
	r := setup(t)

	bodies := []map[string]any{
		{"voyage": "v", "hatch": "h", "tg": 60.5, "ta": 20, "rh": 70}, // range
		{"voyage": "v", "hatch": "h", "tg": 20, "ta": 20, "rh": 0},    // RH below 1
		{"voyage": "v", "hatch": "h", "tg": -21, "ta": 99, "rh": 70},  // two bad fields
		{"voyage": "", "hatch": "h", "tg": 20, "ta": 20, "rh": 70},    // empty voyage
	}
	for _, b := range bodies {
		w := do(t, r, http.MethodPost, "/api/assessments", b)
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotEmpty(t, resp["fields"])
	}

	// Raw non-finite JSON literals are also 422 (not 400): the value
	// 1e999 overflows float64 to +Inf during decode.
	w := doRaw(t, r, `{"voyage":"v","hatch":"h","tg":1e999,"ta":20,"rh":70}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	fields := resp["fields"].([]any)
	require.Len(t, fields, 1)
	assert.Equal(t, "tg", fields[0].(map[string]any)["field"])

	w = do(t, r, http.MethodGet, "/api/assessments", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var list map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.Empty(t, list["items"], "no record may be persisted on 422")
}

// The two critical endpoints delta = +/-2.00 must both decide "retest"
// through the real HTTP + JSON + SQLite stack. Bisection on Tg locates the
// exact float64 verdict transition: the largest Tg still retesting and the
// next float64 (already allowing/denying). The retest side must display
// exactly +/-2.00.
func TestCreate_BandEndpointsRetest(t *testing.T) {
	r := setup(t)

	w := do(t, r, http.MethodPost, "/api/assessments", validBody())
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var first map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &first))
	td := first["td"].(float64)

	post := func(tg float64) map[string]any {
		body := validBody()
		body["tg"] = tg
		w := do(t, r, http.MethodPost, "/api/assessments", body)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		return got
	}

	sweep := func(dir float64, outside string) {
		lo := td + dir*(2.0-1e-6) // safely inside the retest band
		hi := td + dir*(2.0+1e-6) // safely outside
		require.Equal(t, "retest", post(lo)["verdict"])
		require.Equal(t, outside, post(hi)["verdict"])

		// Binary-search the float64 boundary (verdict is monotone in Tg).
		for i := 0; i < 80; i++ {
			mid := (lo + hi) / 2
			if mid == lo || mid == hi {
				break
			}
			if post(mid)["verdict"] == "retest" {
				lo = mid
			} else {
				hi = mid
			}
		}
		lastRetest := post(lo)
		firstStrict := post(hi)
		assert.Equal(t, "retest", lastRetest["verdict"])
		assert.Equal(t, 2.00*dir, lastRetest["delta_display"])
		assert.Equal(t, outside, firstStrict["verdict"])
		// lo and hi are adjacent float64 values: no representable Tg exists
		// between "endpoint retest" and "strictly outside".
		assert.Equal(t, math.Nextafter(lo, dir*math.Inf(1)), hi)
	}

	sweep(+1, "allowed")
	sweep(-1, "denied")
}

// A valid submission keeps showing the same verdict after reload: the POST
// response, GET list and GET detail all agree.
func TestCreate_PersistedVerdictStable(t *testing.T) {
	r := setup(t)
	w := do(t, r, http.MethodPost, "/api/assessments", validBody())
	require.Equal(t, http.StatusCreated, w.Code)
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := strconv.FormatFloat(created["id"].(float64), 'f', 0, 64)

	w = do(t, r, http.MethodGet, "/api/assessments", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var list map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	item := list["items"].([]any)[0].(map[string]any)
	assert.Equal(t, created["verdict"], item["verdict"])
	assert.Equal(t, created["delta"], item["delta"])
	assert.Equal(t, created["td"], item["td"])

	w = do(t, r, http.MethodGet, "/api/assessments/"+id, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var detail map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &detail))
	assert.Equal(t, created["verdict"], detail["verdict"])
	assert.Equal(t, created["delta"], detail["delta"])
	assert.Equal(t, created["formula"], detail["formula"])
}

func TestGet_DetailAndMissing(t *testing.T) {
	r := setup(t)
	w := do(t, r, http.MethodPost, "/api/assessments", validBody())
	require.Equal(t, http.StatusCreated, w.Code)
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	id := strconv.FormatFloat(created["id"].(float64), 'f', 0, 64)
	w = do(t, r, http.MethodGet, "/api/assessments/"+id, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var detail map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &detail))
	assert.Equal(t, "allowed", detail["verdict"])
	assert.Contains(t, detail["formula"].(map[string]any)["gamma_line"], "ln(70/100)")

	w = do(t, r, http.MethodGet, "/api/assessments/9999", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestCreate_MissingAndWrongTypesAre422(t *testing.T) {
	r := setup(t)

	w := do(t, r, http.MethodPost, "/api/assessments", map[string]any{"voyage": "v", "hatch": "h", "tg": 20})
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp["fields"].([]any), 2)

	w = doRaw(t, r, `{"voyage":"v","hatch":"h","tg":"abc","ta":20,"rh":70}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	var wrongType map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrongType))
	wtFields := wrongType["fields"].([]any)
	require.Len(t, wtFields, 1)
	assert.Equal(t, "wrong_type", wtFields[0].(map[string]any)["code"])

	w = doRaw(t, r, `{"voyage":"v","hatch":"h","tg":-1e999,"ta":20,"rh":70}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	var negInf map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &negInf))
	ni := negInf["fields"].([]any)
	require.Len(t, ni, 1)
	assert.Equal(t, "not_finite", ni[0].(map[string]any)["code"])

	// Malformed JSON stays a 400.
	w = doRaw(t, r, `{not json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// A structurally malformed body must be rejected as 400 (request body format
// error) and never be persisted. In particular trailing content behind the
// single JSON object must be caught even when it starts with ']': the old
// Decoder.More() check read a stray ']' as an end-array token and falsely
// reported end-of-stream, so the valid first half of the request was stored.
func TestCreate_TrailingContentIsRejectedAndNotPersisted(t *testing.T) {
	r := setup(t)
	prefix := `{"voyage":"V-100","hatch":"4H","tg":25,"ta":20,"rh":70}`

	for _, suffix := range []string{"]", " ]", " GARBAGE", `{"x":1}`, "123", ",null"} {
		w := doRaw(t, r, prefix+suffix)
		require.Equalf(t, http.StatusBadRequest, w.Code,
			"trailing content %q must be rejected, got %d: %s", suffix, w.Code, w.Body.String())
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		errMsg, _ := resp["error"].(string)
		assert.Contains(t, errMsg, "请求体", "suffix %q reports a body-level error", suffix)
		assert.NotContains(t, resp, "fields", "suffix %q is not reported as field validation", suffix)
	}

	// No leading whitespace tricks either; and the empty body is a format
	// error rather than five "field required" 422s.
	for _, body := range []string{"", "   ", "\n\t"} {
		w := doRaw(t, r, body)
		require.Equal(t, http.StatusBadRequest, w.Code, "whitespace-only body %q", body)
	}

	// A well-formed submission after the rejected ones still succeeds...
	okW := doRaw(t, r, prefix)
	require.Equal(t, http.StatusCreated, okW.Code, okW.Body.String())

	// ...and none of the rejected requests produced a record.
	w, list := getMap(t, r, "/api/assessments")
	require.Equal(t, http.StatusOK, w)
	assert.Len(t, list["items"].([]any), 1, "no trailing-junk request may be persisted")
}

// A repeated key makes the request ambiguous: map unmarshalling silently keeps
// the LAST value, so a field declared twice with different numbers used to be
// accepted (and stored) with the final value. Such bodies must be rejected as
// malformed regardless of whether the two values agree.
func TestCreate_DuplicateKeysAreRejected(t *testing.T) {
	r := setup(t)

	bodies := []string{
		// The reported case: same humidity field twice, different values.
		`{"voyage":"V-100","hatch":"4H","tg":25,"ta":20,"rh":70,"rh":1}`,
		// Whitespace/casing is irrelevant; a temperature field duplicated.
		`{"voyage":"V-100","hatch":"4H","tg":25, "tg":26,"ta":20,"rh":70}`,
		// Even identical values are a structural ambiguity.
		`{"voyage":"V-100","hatch":"4H","tg":25,"ta":20,"rh":70,"rh":70}`,
		// Text fields are covered by the same uniqueness rule.
		`{"voyage":"V-100","voyage":"V-200","hatch":"4H","tg":25,"ta":20,"rh":70}`,
	}
	for _, body := range bodies {
		w := doRaw(t, r, body)
		require.Equalf(t, http.StatusBadRequest, w.Code,
			"duplicate key must be rejected, got %d: %s", w.Code, w.Body.String())
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "重复声明")
		assert.NotContains(t, resp, "fields")
	}

	// Nothing was stored, and the same payload without the duplicate is fine.
	lw, list := getMap(t, r, "/api/assessments")
	require.Equal(t, http.StatusOK, lw)
	assert.Empty(t, list["items"], "duplicate-key requests must never persist")

	cleanW := doRaw(t, r, `{"voyage":"V-100","hatch":"4H","tg":25,"ta":20,"rh":70}`)
	require.Equal(t, http.StatusCreated, cleanW.Code, cleanW.Body.String())
}

// A top-level null (or any non-object JSON document) is a request body format
// error: 400 explaining the top level must be an object — not a 422 that
// invents voyage/hatch/tg/ta/rh "required" errors for a document containing
// none of those fields.
func TestCreate_TopLevelNonObjectIsFormatError(t *testing.T) {
	r := setup(t)

	for _, body := range []string{"null", "[]", "123", `"voyage"`, "true"} {
		w := doRaw(t, r, body)
		require.Equalf(t, http.StatusBadRequest, w.Code,
			"top-level %q must be a 400 format error, got %d", body, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "请求体格式错误", "body %q", body)
		assert.NotContains(t, resp, "fields", "body %q must not fake per-field errors", body)
	}

	// null specifically must not masquerade as five missing fields.
	w := doRaw(t, r, `null`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotContains(t, resp["error"], "必须填写")

	// Structural validation only guards the document shape. A field whose
	// value has the wrong JSON type remains an ordinary 422 field error, so
	// value-level reporting is unchanged.
	w = doRaw(t, r, `{"voyage":"v","hatch":"h","tg":{},"ta":20,"rh":70}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	var wrongType map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrongType))
	wtFields := wrongType["fields"].([]any)
	require.Len(t, wtFields, 1)
	assert.Equal(t, "tg", wtFields[0].(map[string]any)["field"])
	assert.Equal(t, "wrong_type", wtFields[0].(map[string]any)["code"])
}

func TestHealthz(t *testing.T) {
	r := setup(t)
	w := do(t, r, http.MethodGet, "/api/healthz", nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

func submitOK(t *testing.T, r http.Handler, voyage, hatch string, tg float64) map[string]any {
	t.Helper()
	body := map[string]any{"voyage": voyage, "hatch": hatch, "tg": tg, "ta": 20.0, "rh": 70.0}
	w, got := postMap(t, r, body)
	require.Equal(t, http.StatusCreated, w, got)
	return got
}

func getOverview(t *testing.T, r http.Handler, rawPath string) (int, map[string]any) {
	t.Helper()
	w := do(t, r, http.MethodGet, rawPath, nil)
	var got map[string]any
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	}
	return w.Code, got
}

// End-to-end through Gin + SQLite: interleave two voyages and several
// hatches, measuring one hatch repeatedly. The overview for the target
// voyage must contain exactly that voyage's latest row per hatch, in stable
// hatch order, and every row must link to the persisted record.
func TestOverview_InterleavedLatestPerHatch(t *testing.T) {
	r := setup(t)

	// Interleave submissions of V-OV and V-OTHER across shared hatch numbers.
	a1 := submitOK(t, r, "V-OV", "3H", 25)
	submitOK(t, r, "V-OTHER", "3H", 5) // must never appear for V-OV
	b1 := submitOK(t, r, "V-OV", "2P", 24)
	a2 := submitOK(t, r, "V-OV", "3H", 23) // repeat measurement of 3H
	submitOK(t, r, "V-OTHER", "4H", 6)
	a3 := submitOK(t, r, "V-OV", "3H", 26) // latest 3H
	c1 := submitOK(t, r, "V-OV", "4H", 20)

	w, got := getOverview(t, r, "/api/voyages/V-OV/hatches/latest")
	require.Equal(t, http.StatusOK, w, got)
	assert.Equal(t, "V-OV", got["voyage"])

	items, ok := got["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 3, "one snapshot per hatch, nothing from V-OTHER")

	// Stable hatch-ascending order.
	wantHatches := []string{"2P", "3H", "4H"}
	byHatch := map[string]map[string]any{}
	for i, it := range items {
		row := it.(map[string]any)
		assert.Equal(t, wantHatches[i], row["hatch"], "rows are sorted by hatch")
		assert.Equal(t, "V-OV", row["voyage"])
		byHatch[row["hatch"].(string)] = row
	}

	// The repeated hatch resolves to the biggest id (a3), never a1/a2.
	latest3H := byHatch["3H"]
	assert.Equal(t, a3["id"], latest3H["id"])
	assert.NotEqual(t, a1["id"], latest3H["id"])
	assert.NotEqual(t, a2["id"], latest3H["id"])
	assert.Equal(t, a3["verdict"], latest3H["verdict"])
	assert.Equal(t, b1["id"], byHatch["2P"]["id"])
	assert.Equal(t, c1["id"], byHatch["4H"]["id"])

	// The other voyage is independently correct (3H there is the other row).
	w2, other := getOverview(t, r, "/api/voyages/V-OTHER/hatches/latest")
	require.Equal(t, http.StatusOK, w2)
	otherItems := other["items"].([]any)
	require.Len(t, otherItems, 2)
	for _, it := range otherItems {
		assert.Equal(t, "V-OTHER", it.(map[string]any)["voyage"])
	}
}

// Same created_at on every row (including the repeat) must not change which
// record is picked: latestness follows MAX(id).
func TestOverview_SameCreatedAtPicksMaxID(t *testing.T) {
	st, r := setupWithStore(t)
	ctx := context.Background()

	first := submitOK(t, r, "V-TS", "1H", 25)
	second := submitOK(t, r, "V-TS", "1H", 23) // latest by id
	other := submitOK(t, r, "V-TS", "2H", 24)

	// Collapse all timestamps to one identical value through the test seam.
	same := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	for _, id := range []float64{first["id"].(float64), second["id"].(float64), other["id"].(float64)} {
		require.NoError(t, st.SetCreatedAtForTest(ctx, int64(id), same))
	}
	// The later row is even back-dated relative to the earlier one.
	require.NoError(t, st.SetCreatedAtForTest(ctx, int64(second["id"].(float64)),
		same.Add(-time.Hour)))

	w, got := getOverview(t, r, "/api/voyages/V-TS/hatches/latest")
	require.Equal(t, http.StatusOK, w)
	items := got["items"].([]any)
	require.Len(t, items, 2)
	row1 := items[0].(map[string]any)
	assert.Equal(t, "1H", row1["hatch"])
	assert.Equal(t, second["id"], row1["id"],
		"MAX(id) wins even when the latest row has an earlier created_at")
}

// An unknown voyage is a normal 200 with an EMPTY items collection.
func TestOverview_UnknownVoyageReturnsEmptyCollection(t *testing.T) {
	r := setup(t)
	submitOK(t, r, "V-KNOWN", "1H", 25)

	w, got := getOverview(t, r, "/api/voyages/V-MISSING/hatches/latest")
	require.Equal(t, http.StatusOK, w)
	items, ok := got["items"].([]any)
	require.True(t, ok)
	assert.Empty(t, items)
	assert.Equal(t, "V-MISSING", got["voyage"])
}

// An empty voyage path segment is an explicit request error, not a crash.
func TestOverview_EmptyVoyageSegmentIsBadRequest(t *testing.T) {
	r := setup(t)
	w := do(t, r, http.MethodGet, "/api/voyages//hatches/latest", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Contains(t, got["error"], "航次")
}

// A voyage code containing a slash is addressable via its percent-encoded
// form; the value is unescaped before querying, so the row really matches.
func TestOverview_PercentEncodedSlashInVoyage(t *testing.T) {
	r := setup(t)
	created := submitOK(t, r, "V/A-9", "3H", 25)

	w, got := getOverview(t, r, "/api/voyages/V%2FA-9/hatches/latest")
	require.Equal(t, http.StatusOK, w, got)
	items := got["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, created["id"], items[0].(map[string]any)["id"])
	assert.Equal(t, "V/A-9", items[0].(map[string]any)["voyage"])
}

// A genuinely malformed percent-escape is rejected as a 400 request error by
// the HTTP stack before any handler runs. Exercise the real listener because
// httptest.NewRequest rejects the bad URL while building the request.
func TestOverview_MalformedPathEncodingIsBadRequest(t *testing.T) {
	_, r := setupWithStore(t)
	ts := httptest.NewServer(r)
	defer ts.Close()

	for _, target := range []string{
		"/api/voyages/a%zz/hatches/latest",
		"/api/voyages/%/hatches/latest",
	} {
		conn, err := net.Dial("tcp", ts.Listener.Addr().String())
		require.NoError(t, err)
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: probe\r\nConnection: close\r\n\r\n", target)
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, target)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		conn.Close()
	}
}

// The overview snapshot is a lean read-only shape: no formula and no
// comparison block, while POST/list/detail responses keep their old shapes.
func TestOverview_SnapshotShapeDoesNotAffectExistingResponses(t *testing.T) {
	r := setup(t)

	first := submitOK(t, r, "V-SHAPE", "1H", 25)
	second := submitOK(t, r, "V-SHAPE", "1H", 24)

	w, got := getOverview(t, r, "/api/voyages/V-SHAPE/hatches/latest")
	require.Equal(t, http.StatusOK, w)
	snap := got["items"].([]any)[0].(map[string]any)
	for _, key := range []string{"id", "voyage", "hatch", "tg", "ta", "rh",
		"gamma", "td", "delta", "gamma_display", "td_display", "delta_display",
		"verdict", "created_at"} {
		assert.Contains(t, snap, key, "snapshot carries %s", key)
	}
	assert.NotContains(t, snap, "formula", "overview rows omit the formula block")
	assert.NotContains(t, snap, "comparison", "overview rows omit the comparison block")

	// POST response is unchanged (formula present, comparison absent).
	assert.Contains(t, second, "formula")
	assert.NotContains(t, second, "comparison")

	// List items keep their old shape.
	wl, list := getMap(t, r, "/api/assessments")
	require.Equal(t, http.StatusOK, wl)
	for _, it := range list["items"].([]any) {
		row := it.(map[string]any)
		assert.Contains(t, row, "formula")
		assert.NotContains(t, row, "comparison")
	}

	// Detail of the repeat still carries its comparison block.
	wd, detail := getMap(t, r, "/api/assessments/"+
		strconv.FormatFloat(second["id"].(float64), 'f', 0, 64))
	require.Equal(t, http.StatusOK, wd)
	assert.Contains(t, detail, "comparison")
	assert.Equal(t, true, detail["comparison"].(map[string]any)["available"])
	assert.Equal(t, first["id"],
		detail["comparison"].(map[string]any)["previous"].(map[string]any)["id"])
}

// Guard against accidental constant drift.
func TestConstants(t *testing.T) {
	assert.Equal(t, -20.0, decision.MinTempC)
	assert.Equal(t, 60.0, decision.MaxTempC)
	assert.Equal(t, 1.0, decision.MinRH)
	assert.Equal(t, 100.0, decision.MaxRH)
}
