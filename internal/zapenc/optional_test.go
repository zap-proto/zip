package zapenc_test

import (
	"reflect"
	"testing"

	"github.com/zap-proto/zip/internal/zapenc"
)

type cents int64

type optional struct {
	B   *bool
	I8  *int8
	I16 *int16
	I32 *int32
	I64 *int64
	I   *int
	U8  *uint8
	U16 *uint16
	U32 *uint32
	U64 *uint64
	U   *uint
	F32 *float32
	F64 *float64
	S   *string
	Raw *[]byte
	C   *cents
	In  *inner
}

func ptr[T any](v T) *T { return &v }

// TestAPointerIsPresentOrAbsent round-trips every optional kind three ways: nil
// stays nil, a pointer to zero stays a pointer to zero, a pointer to a value
// keeps the value. The middle row is the one a scalar slot cannot carry: enforce
// pointing at false arrived unset and every new spend cap was created hard.
func TestAPointerIsPresentOrAbsent(t *testing.T) {
	cases := map[string]optional{
		"nil": {},
		"zero": {
			B: ptr(false), I8: ptr(int8(0)), I16: ptr(int16(0)), I32: ptr(int32(0)),
			I64: ptr(int64(0)), I: ptr(0), U8: ptr(uint8(0)), U16: ptr(uint16(0)),
			U32: ptr(uint32(0)), U64: ptr(uint64(0)), U: ptr(uint(0)),
			F32: ptr(float32(0)), F64: ptr(0.0), S: ptr(""), Raw: ptr([]byte{}),
			C: ptr(cents(0)), In: &inner{},
		},
		"value": {
			B: ptr(true), I8: ptr(int8(-8)), I16: ptr(int16(-300)), I32: ptr(int32(-70000)),
			I64: ptr(int64(-5_000_000_000)), I: ptr(-1), U8: ptr(uint8(200)), U16: ptr(uint16(60000)),
			U32: ptr(uint32(4_000_000_000)), U64: ptr(uint64(1) << 63), U: ptr(uint(7)),
			F32: ptr(float32(3.5)), F64: ptr(-2.25), S: ptr("set"), Raw: ptr([]byte{0, 1}),
			C: ptr(cents(163357)), In: &inner{Slug: "s", Listed: true},
		},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			b, err := zapenc.Marshal(&want)
			if err != nil {
				t.Fatal(err)
			}
			var got optional
			if err := zapenc.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			w, g := reflect.ValueOf(want), reflect.ValueOf(got)
			for i := range w.NumField() {
				field := w.Type().Field(i).Name
				wf, gf := w.Field(i), g.Field(i)
				if wf.IsNil() != gf.IsNil() {
					t.Errorf("%s: sent nil=%v, read nil=%v", field, wf.IsNil(), gf.IsNil())
					continue
				}
				if wf.IsNil() {
					continue
				}
				if field == "Raw" { // a present empty []byte may read back as a nil slice
					if string(*want.Raw) != string(*got.Raw) {
						t.Errorf("Raw: sent %v, read %v", *want.Raw, *got.Raw)
					}
					continue
				}
				if !reflect.DeepEqual(wf.Elem().Interface(), gf.Elem().Interface()) {
					t.Errorf("%s: sent %v, read %v", field, wf.Elem(), gf.Elem())
				}
			}
		})
	}
}
