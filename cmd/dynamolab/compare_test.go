package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"aws-systems-performance/internal/dynamolab"
)

func TestComparisonOrderAndWarmups(t *testing.T) {
	var calls []string
	counts := map[string]int{}
	measure := func(_ context.Context, method, _ string, _ int32) (dynamolab.Measurement, error) {
		calls = append(calls, method)
		counts[method]++
		code := "user-result"
		if strings.HasPrefix(method, "code_") {
			code = "code-result"
		}
		return dynamolab.Measurement{Method: method, Returned: 1, Items: []dynamolab.Item{{Code: code}}, ElapsedMS: float64(counts[method])}, nil
	}
	runs, medians, err := runComparison(context.Background(), measure, "user", "code", 2, 4, 2, 123)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 24 || len(runs) != 4 {
		t.Fatalf("calls=%d runs=%d", len(calls), len(runs))
	}
	for _, run := range runs {
		if run.UserScan.ElapsedMS < 3 {
			t.Fatal("warm-up included in results")
		}
	}
	for method, value := range medians {
		if value != 4.5 {
			t.Errorf("median %s=%v; warm-ups should be excluded", method, value)
		}
	}
	for i := 1; i < len(runs); i++ {
		if reflect.DeepEqual(runs[i-1].ExecutionOrder, runs[i].ExecutionOrder) {
			t.Fatalf("order did not change: %v", runs[i].ExecutionOrder)
		}
	}
	for i := 0; i < 4; i++ {
		seen := map[string]bool{}
		for _, run := range runs {
			seen[run.ExecutionOrder[i]] = true
		}
		if len(seen) != 4 {
			t.Fatalf("position %d is not balanced", i)
		}
	}
	again, _, err := runComparison(context.Background(), measure, "user", "code", 2, 4, 2, 123)
	if err != nil {
		t.Fatal(err)
	}
	for i := range runs {
		if !reflect.DeepEqual(runs[i].ExecutionOrder, again[i].ExecutionOrder) {
			t.Fatal("seed did not reproduce order")
		}
	}
}

func TestComparisonRejectsMismatchedResults(t *testing.T) {
	measure := func(_ context.Context, method, _ string, _ int32) (dynamolab.Measurement, error) {
		code := "same"
		if method == "user_query" {
			code = "different"
		}
		return dynamolab.Measurement{Method: method, Returned: 1, Items: []dynamolab.Item{{Code: code}}}, nil
	}
	_, _, err := runComparison(context.Background(), measure, "user", "code", 0, 1, 0, 1)
	if err == nil || !strings.Contains(err.Error(), "result mismatch") {
		t.Fatalf("got %v", err)
	}
}

func TestMedian(t *testing.T) {
	for _, tc := range []struct {
		in   []float64
		want float64
	}{{nil, 0}, {[]float64{9}, 9}, {[]float64{8, 1, 4}, 4}, {[]float64{10, 1, 6, 2}, 4}} {
		t.Run(fmt.Sprint(tc.in), func(t *testing.T) {
			before := append([]float64(nil), tc.in...)
			if got := median(tc.in); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			if !reflect.DeepEqual(before, tc.in) {
				t.Fatal("median mutated input")
			}
		})
	}
}
