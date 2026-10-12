package bench

import (
	"flag"
	"fmt"
)

var comparisonKeyType = flag.String("bench-key-type", "uint64", "key/value types for all seven containers: uint64 or string")

func validateComparisonKeyType(kind string) error {
	if kind != "uint64" && kind != "string" {
		return fmt.Errorf("bench-key-type must be uint64 or string, got %q", kind)
	}
	return nil
}

func comparisonTypeDescription(kind string) string {
	if kind == "uint64" {
		return "all seven containers use native uint64 keys and values"
	}
	return "all seven containers use pre-encoded 8-byte string keys and values (no timed encoding/decoding)"
}

func tablePointCases(n int, kind string) []comparisonBenchmark {
	if err := validateComparisonKeyType(kind); err != nil {
		panic(err)
	}
	if kind == "string" {
		return comparisonPointCases(n)
	}
	return nativePointCases(n)
}

func tableIterationCases(n int, kind string) []comparisonBenchmark {
	if err := validateComparisonKeyType(kind); err != nil {
		panic(err)
	}
	if kind == "string" {
		return comparisonIterationCases(n)
	}
	return nativeIterationCases(n)
}
