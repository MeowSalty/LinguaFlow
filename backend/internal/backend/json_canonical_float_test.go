package backend_test

import (
	"math"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestBoundedJSONCanonicalFiniteNumbers(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  string
	}{
		{math.Copysign(0, -1), "0"}, {float64(0), "0"}, {float64(1e18), "1000000000000000000"},
		{-0x1p63, "-9223372036854775808"}, {float64(0x1p63), "9223372036854775808"},
		{float64(0x1p64), "1.8446744073709552e+19"}, {float64(1e30), "1e+30"},
		{-math.SmallestNonzeroFloat64, "-5e-324"}, {int64(9007199254740993), "9007199254740993"},
		{uint64(math.MaxUint64), "18446744073709551615"}, {float32(1e18), "999999984306749440"},
		{float32(0x1p64), "1.8446744073709552e+19"}, {float32(math.MaxFloat32), "3.4028234663852886e+38"},
		{float32(math.SmallestNonzeroFloat32), "1.401298464324817e-45"}, {float32(0.1), "0.10000000149011612"},
	} {
		b, err := backend.NewBuildBounds(1024, 1024)
		if err != nil {
			t.Fatal(err)
		}
		measured, err := b.MeasureJSON(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := b.EncodeJSON(tc.value, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want || measured != int64(len(got)) {
			t.Fatalf("%v: encode=%s measured=%d want=%s", tc.value, got, measured, tc.want)
		}
		roundtrip, err := workstate.CanonicalCandidateJSON(got, 1024)
		if err != nil || string(roundtrip) != string(got) {
			t.Fatalf("canonical SQL roundtrip %v: got=%s canonical=%s err=%v", tc.value, got, roundtrip, err)
		}
	}
}
