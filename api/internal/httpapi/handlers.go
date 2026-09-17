// Package httpapi exposes the JSON API. The handlers never compute the
// dew-point themselves: decision.Evaluate is the only implementation and
// the page renders exactly what the API returns.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"grain-ventilation/internal/decision"
	"grain-ventilation/internal/store"
)

// NewRouter builds the Gin engine with all routes.
func NewRouter(st *store.Store) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Match :voyage against the RAW (still percent-encoded) path and then
	// unescape the captured value, so a voyage code containing a slash
	// (e.g. "V/A") is reachable as /api/voyages/V%2FA/hatches/latest instead
	// of silently turning into two path segments and 404. A genuinely invalid
	// escape such as %zz never reaches Gin: net/http answers 400 itself.
	r.UseRawPath = true
	r.UnescapePathValues = true
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	api := r.Group("/api")
	{
		api.GET("/healthz", healthz)
		api.POST("/assessments", createAssessment(st))
		// The batch route is a static child of /assessments, so it cannot be
		// captured by the GET /assessments/:id detail route.
		api.POST("/assessments/batch", createAssessmentBatch(st))
		api.GET("/assessments", listAssessments(st))
		api.GET("/assessments/:id", getAssessment(st))
		api.GET("/voyages/:voyage/hatches/latest", latestHatches(st))
	}
	return r
}

func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// maxBatchRows bounds one batch submission: the chief officer transcribes at
// most twenty measurements taken just before berthing in a single request.
const maxBatchRows = 20

func createAssessment(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := readSingleObjectBody(c)
		if !ok {
			return
		}
		if rejectUnknownFields(c, raw) {
			return
		}

		in, res, bad, parseErr := parseAssessmentRow(raw)
		if parseErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": parseErr.Error()})
			return
		}
		if len(bad) > 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "输入校验失败，未生成任何记录",
				"fields": bad,
			})
			return
		}

		a, err := st.Create(c.Request.Context(), in, *res)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusCreated, toDTO(a))
	}
}

// rowFieldError is a single rejected field of a batch row. It is the original
// decision.FieldError payload plus the 1-based row number inside the ordered
// measurements array, so the page can keep every input and scroll to the
// offending row.
type rowFieldError struct {
	Row     int    `json:"row"`
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// createAssessmentBatch accepts up to maxBatchRows measurements in one
// request: {"measurements": [ {voyage,hatch,tg,ta,rh}, ... ]}. Every row is
// parsed and validated with the SAME logic as the single-row endpoint
// (parseAssessmentRow + decision.Evaluate): identical field codes, messages
// and dew-point verdict. If ANY row is illegal, no row is saved and the 422
// lists every offending field with its 1-based row number. A valid batch is
// stored row by row, strictly in array order, in one transaction; see
// store.CreateMany for the in-batch predecessor rules.
func createAssessmentBatch(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := readEnvelopeObjectBody(c)
		if !ok {
			return
		}
		// The wrapper carries exactly one key; envelope mistakes are request
		// format errors (400), not per-row field validation (422).
		for k := range raw {
			if k != "measurements" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "出现未知字段: " + k})
				return
			}
		}
		mb, present := raw["measurements"]
		if !present || string(mb) == "null" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": `请求体格式错误：缺少 "measurements" 对象数组`,
			})
			return
		}
		var elems []json.RawMessage
		if err := json.Unmarshal(mb, &elems); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": `请求体格式错误："measurements" 必须是 JSON 对象数组: ` + err.Error(),
			})
			return
		}
		if len(elems) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "请求体格式错误：measurements 至少包含一行测量",
			})
			return
		}
		if len(elems) > maxBatchRows {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("请求体格式错误：批量测量最多 %d 行（收到 %d 行）",
					maxBatchRows, len(elems)),
			})
			return
		}

		// Each row must itself be exactly one JSON object: the same unique-key
		// and no-trailing-junk rules as a standalone body, reported per row.
		rowMaps := make([]map[string]json.RawMessage, len(elems))
		for i, elem := range elems {
			if err := validateJSONObjectBody(elem); err != nil {
				// Strip the inner message's own body-level prefix so the row
				// number reads naturally ("第 2 行：…").
				msg := strings.TrimPrefix(err.Error(), "请求体格式错误：")
				msg = strings.TrimPrefix(msg, "请求体不是合法的 JSON: ")
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("请求体格式错误：第 %d 行：%s", i+1, msg),
				})
				return
			}
			var rm map[string]json.RawMessage
			if err := json.Unmarshal(elem, &rm); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("请求体格式错误：第 %d 行不是合法的 JSON: %v", i+1, err),
				})
				return
			}
			for k := range rm {
				if !allowedAssessmentFields[k] {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": fmt.Sprintf("第 %d 行出现未知字段: %s", i+1, k),
					})
					return
				}
			}
			rowMaps[i] = rm
		}

		// Validate ALL rows first, collecting every offending field, so a
		// single rejected batch reports every problem row at once and (more
		// importantly) never reaches the store.
		ms := make([]store.ValidatedMeasurement, 0, len(rowMaps))
		var bad []rowFieldError
		for i, rm := range rowMaps {
			in, res, rowErrs, evalErr := parseAssessmentRow(rm)
			if evalErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": evalErr.Error()})
				return
			}
			for _, fe := range rowErrs {
				bad = append(bad, rowFieldError{
					Row: i + 1, Field: fe.Field, Code: fe.Code, Message: fe.Message,
				})
			}
			if len(rowErrs) == 0 {
				ms = append(ms, store.ValidatedMeasurement{Input: in, Result: *res})
			}
		}
		if len(bad) > 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "批量输入校验失败，未生成任何记录",
				"fields": bad,
			})
			return
		}

		// One transaction for the whole ordered batch: a failure on any row
		// rolls back every earlier row and their predecessor links.
		created, err := st.CreateMany(c.Request.Context(), ms)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "批量保存失败: " + err.Error()})
			return
		}
		items := make([]gin.H, 0, len(created))
		for _, a := range created {
			// The per-row response item is byte-for-byte the single-row DTO
			// (formula included, comparison never attached on writes).
			items = append(items, toDTO(a))
		}
		c.JSON(http.StatusCreated, gin.H{"items": items})
	}
}

// allowedAssessmentFields are the only legal fields of one measurement object,
// shared by the single-row and batch endpoints.
var allowedAssessmentFields = map[string]bool{
	"voyage": true,
	"hatch":  true,
	"tg":     true,
	"ta":     true,
	"rh":     true,
}

// readSingleObjectBody reads the body and applies the shared structural
// validation: it must be exactly one JSON object with no trailing bytes and
// no repeated keys (at ANY nesting level). On any failure the response (400)
// has already been written and ok is false; on success the object's raw
// field values are returned for per-field parsing.
func readSingleObjectBody(c *gin.Context) (map[string]json.RawMessage, bool) {
	body, ok := readRequestBody(c)
	if !ok {
		return nil, false
	}
	if err := validateJSONObjectBody(body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	return decodeObjectBody(c, body)
}

// readEnvelopeObjectBody is the batch counterpart: it requires exactly one
// top-level JSON object with no trailing bytes and unique TOP-LEVEL keys, but
// deliberately does NOT enforce structural rules inside nested frames. The
// nested values are the individual measurement rows, which are validated
// separately (strictly) per row so a structural error can carry its row
// number; malformed JSON anywhere still fails to decode here.
func readEnvelopeObjectBody(c *gin.Context) (map[string]json.RawMessage, bool) {
	body, ok := readRequestBody(c)
	if !ok {
		return nil, false
	}
	if err := validateTopLevelJSONObject(body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	return decodeObjectBody(c, body)
}

func readRequestBody(c *gin.Context) ([]byte, bool) {
	if c.Request.Body == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体为空，必须提交 JSON 对象"})
		return nil, false
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求体失败: " + err.Error()})
		return nil, false
	}
	return body, true
}

func decodeObjectBody(c *gin.Context, body []byte) (map[string]json.RawMessage, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "请求体不是合法的 JSON",
			"details": err.Error(),
		})
		return nil, false
	}
	return raw, true
}

// rejectUnknownFields answers 400 when the object carries any field outside
// the five measurement fields. It returns true when the caller must stop.
func rejectUnknownFields(c *gin.Context, raw map[string]json.RawMessage) bool {
	for k := range raw {
		if !allowedAssessmentFields[k] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "出现未知字段: " + k})
			return true
		}
	}
	return false
}

// parseAssessmentRow parses and validates ONE measurement object, reused by
// the single-row endpoint and every row of a batch. Values decode into raw
// messages first so one bad field does not abort the others: every offending
// field is returned in one pass with its original code/message. Range checks
// (and empty-string checks) then run through decision.Evaluate, so batch and
// single rows can never diverge from the math package. On success the
// computed result (unrounded intermediates + verdict) is returned.
func parseAssessmentRow(raw map[string]json.RawMessage) (decision.Input, *decision.Result, []decision.FieldError, error) {
	var in decision.Input
	var bad []decision.FieldError

	parseString := func(field string, dst *string, label string) {
		b, ok := raw[field]
		if !ok || string(b) == "null" {
			bad = append(bad, decision.FieldError{Field: field, Code: "required", Message: label + "必须填写"})
			return
		}
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			bad = append(bad, decision.FieldError{Field: field, Code: "wrong_type", Message: label + "必须是字符串"})
			return
		}
		*dst = strings.TrimSpace(s)
	}
	parseNumber := func(field string, dst *float64, label string) {
		b, ok := raw[field]
		if !ok || string(b) == "null" {
			bad = append(bad, decision.FieldError{Field: field, Code: "required", Message: label + "必须填写"})
			return
		}
		var v float64
		if err := json.Unmarshal(b, &v); err != nil {
			// A JSON number that fails to decode (e.g. 1e999 -> +Inf)
			// is non-finite; any other JSON value is the wrong type.
			var ute *json.UnmarshalTypeError
			if errors.As(err, &ute) && strings.HasPrefix(ute.Value, "number") {
				bad = append(bad, decision.FieldError{Field: field, Code: "not_finite", Message: label + "必须为有限数值"})
			} else {
				bad = append(bad, decision.FieldError{Field: field, Code: "wrong_type", Message: label + "必须是数值"})
			}
			return
		}
		*dst = v
	}

	parseString("voyage", &in.Voyage, "航次代号")
	parseString("hatch", &in.Hatch, "舱号")
	parseNumber("tg", &in.Tg, "粮温 Tg")
	parseNumber("ta", &in.Ta, "舱内气温 Ta")
	parseNumber("rh", &in.RH, "相对湿度 RH")

	res, err := decision.Evaluate(in)
	var verr *decision.ValidationError
	if errors.As(err, &verr) {
		// Missing fields keep their zero value, which may spuriously fail a
		// range check too, so each field is reported exactly once.
		seen := map[string]bool{}
		for _, f := range bad {
			seen[f.Field] = true
		}
		for _, f := range verr.Fields {
			if !seen[f.Field] {
				bad = append(bad, f)
				seen[f.Field] = true
			}
		}
	} else if err != nil {
		return in, nil, nil, err
	}
	if len(bad) > 0 {
		return in, nil, bad, nil
	}
	return in, res, nil, nil
}

// jsonContainer is one object/array frame while structurally walking a body.
type jsonContainer struct {
	isObject bool
	wantKey  bool            // objects only: the next string token is a key
	keys     map[string]bool // keys already declared in this object
}

// validateJSONObjectBody verifies body is exactly one JSON object and nothing
// else. It enforces structural rules that decoding straight into a map cannot:
//
//   - no bytes may follow the object. Decoder.More() reads a stray ']' as an
//     end-array token and falsely reports end-of-stream, so trailing brackets
//     used to reach persistence;
//   - object keys must be unique. Map unmarshalling keeps the LAST value of a
//     repeated key, so an ambiguous request (same field declared twice with
//     different numbers) used to be accepted with the final value;
//   - the top level must be an object. A top-level null (or array/scalar) is a
//     malformed body, not five spurious "field required" validation errors.
//
// Field-level value and range checks are deliberately left to the later 422
// path so every offending field is still reported at once. UseNumber keeps
// numeric literals such as 1e999 intact during the walk, letting them reach
// the per-field not_finite check instead of failing here.
func validateJSONObjectBody(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	first, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("请求体为空，必须提交 JSON 对象")
		}
		return fmt.Errorf("请求体不是合法的 JSON: %v", err)
	}
	if d, ok := first.(json.Delim); !ok || d != '{' {
		if first == nil {
			return errors.New("请求体格式错误：请求体为 null，顶层必须是包含评估字段的 JSON 对象")
		}
		return errors.New("请求体格式错误：顶层必须是 JSON 对象")
	}

	frames := []jsonContainer{{isObject: true, wantKey: true, keys: map[string]bool{}}}
	for len(frames) > 0 {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("请求体不是合法的 JSON: %v", err)
		}
		top := &frames[len(frames)-1]
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{', '[':
				f := jsonContainer{isObject: delim == '{', wantKey: delim == '{'}
				if f.isObject {
					f.keys = map[string]bool{}
				}
				frames = append(frames, f)
			case '}', ']':
				frames = frames[:len(frames)-1]
				// The closed container was one value of its parent object;
				// the parent's next token (if any) is another key.
				if len(frames) > 0 && frames[len(frames)-1].isObject {
					frames[len(frames)-1].wantKey = true
				}
			}
			continue
		}
		if s, isString := tok.(string); isString && top.isObject && top.wantKey {
			if top.keys[s] {
				return fmt.Errorf("请求体格式错误：字段 %q 重复声明，请求含义不唯一", s)
			}
			top.keys[s] = true
			top.wantKey = false // the value token follows
			continue
		}
		if top.isObject {
			top.wantKey = true // scalar value consumed; next token is a key
		}
	}

	// The root object has closed. Anything left in the stream is trailing
	// junk; the next Token call (unlike Decoder.More) also catches a stray
	// ']' that the scanner reports as an error.
	if _, err := dec.Token(); err != io.EOF {
		if err != nil {
			return fmt.Errorf("请求体在单个 JSON 对象后含有非法内容: %v", err)
		}
		return errors.New("请求体在单个 JSON 对象后含有多余内容")
	}
	return nil
}

// validateTopLevelJSONObject is the looser structural check for an envelope
// whose only job is to carry nested objects (the batch wrapper): the document
// must be exactly one JSON object with no trailing junk and unique TOP-LEVEL
// keys, but keys of nested frames are not inspected. Nested documents must be
// validated by the caller (each batch row goes through the strict
// validateJSONObjectBody on its own bytes so row-internal errors carry the
// offending row number).
func validateTopLevelJSONObject(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	first, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("请求体为空，必须提交 JSON 对象")
		}
		return fmt.Errorf("请求体不是合法的 JSON: %v", err)
	}
	if d, ok := first.(json.Delim); !ok || d != '{' {
		if first == nil {
			return errors.New("请求体格式错误：请求体为 null，顶层必须是包含 measurements 数组的 JSON 对象")
		}
		return errors.New("请求体格式错误：顶层必须是 JSON 对象")
	}

	// Same frame walk as validateJSONObjectBody, but key uniqueness is only
	// enforced on the root frame; nested key handling still tracks wantKey so
	// values and keys are not confused while skipping the nested structure.
	frames := []jsonContainer{{isObject: true, wantKey: true, keys: map[string]bool{}}}
	for len(frames) > 0 {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("请求体不是合法的 JSON: %v", err)
		}
		top := &frames[len(frames)-1]
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{', '[':
				f := jsonContainer{isObject: delim == '{', wantKey: delim == '{'}
				if f.isObject {
					f.keys = map[string]bool{}
				}
				frames = append(frames, f)
			case '}', ']':
				frames = frames[:len(frames)-1]
				if len(frames) > 0 && frames[len(frames)-1].isObject {
					frames[len(frames)-1].wantKey = true
				}
			}
			continue
		}
		if s, isString := tok.(string); isString && top.isObject && top.wantKey {
			if len(frames) == 1 {
				if top.keys[s] {
					return fmt.Errorf("请求体格式错误：字段 %q 重复声明，请求含义不唯一", s)
				}
				top.keys[s] = true
			}
			top.wantKey = false
			continue
		}
		if top.isObject {
			top.wantKey = true
		}
	}

	if _, err := dec.Token(); err != io.EOF {
		if err != nil {
			return fmt.Errorf("请求体在单个 JSON 对象后含有非法内容: %v", err)
		}
		return errors.New("请求体在单个 JSON 对象后含有多余内容")
	}
	return nil
}

func listAssessments(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		list, err := st.List(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out := make([]gin.H, 0, len(list))
		for _, a := range list {
			out = append(out, toDTO(a))
		}
		c.JSON(http.StatusOK, gin.H{"items": out})
	}
}

// latestHatches serves the read-only voyage "hatch overview": one latest
// snapshot per hatch of the voyage. It never mutates data and never returns
// the per-detail comparison block; the store chooses the single latest row
// per hatch by MAX(id). An unknown voyage is a normal 200 with an empty
// items collection; only a malformed request (empty voyage segment) is a 4xx.
func latestHatches(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		voyage := c.Param("voyage")
		if voyage == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求路径错误：航次代号路径段不能为空"})
			return
		}
		list, err := st.LatestByVoyage(c.Request.Context(), voyage)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out := make([]gin.H, 0, len(list))
		for _, a := range list {
			out = append(out, toSnapshotDTO(a))
		}
		c.JSON(http.StatusOK, gin.H{"voyage": voyage, "items": out})
	}
}

// toSnapshotDTO renders one latest-per-hatch row for the voyage overview. It
// is deliberately a leaner, read-only shape than toDTO: no formula block
// (that lives on the detail page the row links to) and no comparison block
// (which exists only on detail responses).
func toSnapshotDTO(a *store.Assessment) gin.H {
	r := a.Result
	return gin.H{
		"id":            a.ID,
		"voyage":        a.Input.Voyage,
		"hatch":         a.Input.Hatch,
		"tg":            a.Input.Tg,
		"ta":            a.Input.Ta,
		"rh":            a.Input.RH,
		"gamma":         r.Gamma,
		"td":            r.Td,
		"delta":         r.Delta,
		"gamma_display": r.GammaDisplay,
		"td_display":    r.TdDisplay,
		"delta_display": r.DeltaDisplay,
		"verdict":       r.Verdict,
		"created_at":    a.CreatedAt.Format(time.RFC3339Nano),
	}
}

func getAssessment(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		a, err := st.Get(c.Request.Context(), id)
		if errors.Is(err, store.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		dto := toDTO(a)
		// The detail endpoint additionally compares this assessment against
		// the predecessor link fixed when it was created. A stale link
		// (row deleted, or pointing at another voyage/hatch) never hides the
		// current assessment: it is returned with comparison.available=false.
		if a.HasPrev {
			dto["comparison"] = buildComparison(c.Request.Context(), st, a)
		}
		c.JSON(http.StatusOK, dto)
	}
}

// toDTO renders one assessment for the client, including the fully
// substituted formula lines so the detail page shows the API's own
// arithmetic instead of recomputing it in JavaScript.
func toDTO(a *store.Assessment) gin.H {
	r := a.Result
	return gin.H{
		"id":            a.ID,
		"voyage":        a.Input.Voyage,
		"hatch":         a.Input.Hatch,
		"tg":            a.Input.Tg,
		"ta":            a.Input.Ta,
		"rh":            a.Input.RH,
		"gamma":         r.Gamma,
		"td":            r.Td,
		"delta":         r.Delta,
		"gamma_display": r.GammaDisplay,
		"td_display":    r.TdDisplay,
		"delta_display": r.DeltaDisplay,
		"verdict":       r.Verdict,
		"formula":       buildFormula(a),
		"created_at":    a.CreatedAt.Format(time.RFC3339Nano),
	}
}

// num prints a float the shortest way that round-trips; whole numbers keep
// no trailing ".0" noise inside the substituted expressions.
func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// raw prints a full-precision computed intermediate for the audit trail.
func raw(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }

func buildFormula(a *store.Assessment) gin.H {
	in, r := a.Input, a.Result
	return gin.H{
		// γ = ln(RH/100) + 17.62·Ta/(243.12+Ta)
		"gamma_line": "γ = ln(RH/100) + 17.62 × Ta / (243.12 + Ta) = ln(" +
			num(in.RH) + "/100) + 17.62 × " + num(in.Ta) + " / (243.12 + " +
			num(in.Ta) + ") = " + raw(r.Gamma) + "（展示值 " + num(r.GammaDisplay) + "）",
		// Td = 243.12·γ/(17.62-γ)
		"td_line": "Td = 243.12 × γ / (17.62 − γ) = 243.12 × " + raw(r.Gamma) +
			" / (17.62 − " + raw(r.Gamma) + ") = " + raw(r.Td) +
			" ℃（展示值 " + num(r.TdDisplay) + " ℃）",
		// Δ = Tg - Td, verdict on the UNROUNDED delta.
		"delta_line": "Δ = Tg − Td = " + num(in.Tg) + " − " + raw(r.Td) + " = " +
			raw(r.Delta) + " ℃（展示值 " + num(r.DeltaDisplay) + " ℃）",
		"rule_line": "判定以未舍入 Δ 为准：Δ > 2.00 允许通风；Δ < −2.00 禁止通风；" +
			"−2.00 ≤ Δ ≤ 2.00（含两端点）暂停并复测。",
	}
}

// buildComparison resolves the predecessor link saved with this assessment
// and returns the traceable comparison block for the detail response. The
// browser only renders these numbers: every unrounded delta (change) is
// computed here in Go.
//
// If the saved predecessor no longer exists, or no longer belongs to the
// same voyage and hatch, the current assessment is still returned and the
// comparison is marked unavailable. The link is never rebound to another
// record on the fly.
func buildComparison(ctx context.Context, st *store.Store, a *store.Assessment) gin.H {
	prev, err := st.Get(ctx, a.PrevID)
	if errors.Is(err, store.ErrNoRows) {
		return unavailable(a.PrevID, "保存的前序记录已不存在，无法形成对照")
	}
	if err != nil {
		return gin.H{
			"available": false,
			"prev_id":   a.PrevID,
			"reason":    "读取前序记录失败: " + err.Error(),
		}
	}
	if prev.Input.Voyage != a.Input.Voyage || prev.Input.Hatch != a.Input.Hatch {
		return unavailable(a.PrevID,
			"保存的前序记录不属于同一航次同一舱位，对照不可用；未临时改绑其他记录")
	}

	return gin.H{
		"available": true,
		"previous":  prevSummary(prev),
		// Unrounded current − previous for the five quantities the chief
		// officer compares between consecutive measurements of one hatch.
		"changes": gin.H{
			"tg":    a.Input.Tg - prev.Input.Tg,         // 粮温变化
			"ta":    a.Input.Ta - prev.Input.Ta,         // 气温变化
			"rh":    a.Input.RH - prev.Input.RH,         // 湿度变化
			"td":    a.Result.Td - prev.Result.Td,       // 露点变化
			"delta": a.Result.Delta - prev.Result.Delta, // 温差变化
		},
	}
}

func unavailable(prevID int64, reason string) gin.H {
	return gin.H{
		"available": false,
		"prev_id":   prevID,
		"reason":    reason,
	}
}

// prevSummary is the optional predecessor digest attached to a detail
// response. It carries only display/audit fields, never another nested
// comparison, so the payload stays one level deep.
func prevSummary(p *store.Assessment) gin.H {
	return gin.H{
		"id":            p.ID,
		"voyage":        p.Input.Voyage,
		"hatch":         p.Input.Hatch,
		"tg":            p.Input.Tg,
		"ta":            p.Input.Ta,
		"rh":            p.Input.RH,
		"gamma":         p.Result.Gamma,
		"td":            p.Result.Td,
		"delta":         p.Result.Delta,
		"gamma_display": p.Result.GammaDisplay,
		"td_display":    p.Result.TdDisplay,
		"delta_display": p.Result.DeltaDisplay,
		"verdict":       p.Result.Verdict,
		"created_at":    p.CreatedAt.Format(time.RFC3339Nano),
	}
}
