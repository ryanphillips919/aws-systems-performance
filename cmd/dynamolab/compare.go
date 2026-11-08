package main

import (
	"context"
	"fmt"
	"math/rand"
	"sort"

	"aws-systems-performance/internal/dynamolab"
)

type compareRun struct {
	Iteration                              int      `json:"iteration"`
	ExecutionOrder                         []string `json:"execution_order"`
	UserScan, UserQuery, CodeScan, CodeGet dynamolab.Measurement
}

type measureFunc func(context.Context, string, string, int32) (dynamolab.Measurement, error)

// A seeded starting permutation followed by rotation ensures adjacent runs use
// different orders and, over four runs, each operation takes every position.
func runComparison(ctx context.Context, measure measureFunc, user, code string, pageLimit int32, runs, warmups int, seed int64) ([]compareRun, map[string]float64, error) {
	if runs < 1 || warmups < 0 {
		return nil, nil, fmt.Errorf("runs must be positive and warmups nonnegative")
	}
	methods := []string{"user_scan", "user_query", "code_scan", "code_get"}
	random := rand.New(rand.NewSource(seed))
	random.Shuffle(len(methods), func(i, j int) { methods[i], methods[j] = methods[j], methods[i] })
	results := make([]compareRun, 0, runs)
	samples := make(map[string][]float64, len(methods))
	for iteration := 0; iteration < warmups+runs; iteration++ {
		order := make([]string, len(methods))
		values := make(map[string]dynamolab.Measurement, len(methods))
		for j := range order {
			method := methods[(iteration+j)%len(methods)]
			order[j] = method
			value := user
			if method == "code_scan" || method == "code_get" {
				value = code
			}
			measurement, err := measure(ctx, method, value, pageLimit)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", method, err)
			}
			values[method] = measurement
		}
		scan, query, codeScan, codeGet := values["user_scan"], values["user_query"], values["code_scan"], values["code_get"]
		if !dynamolab.EqualItems(scan.Items, query.Items) || !dynamolab.EqualItems(codeScan.Items, codeGet.Items) {
			return nil, nil, fmt.Errorf("result mismatch (GSI propagation or concurrent writes may be responsible); retry on a stable dataset")
		}
		if scan.Returned == 0 || codeGet.Returned != 1 {
			return nil, nil, fmt.Errorf("expected seeded user and code were not found")
		}
		if iteration < warmups {
			continue
		}
		for _, method := range methods {
			samples[method] = append(samples[method], values[method].ElapsedMS)
		}
		results = append(results, compareRun{Iteration: iteration - warmups + 1, ExecutionOrder: order, UserScan: scan, UserQuery: query, CodeScan: codeScan, CodeGet: codeGet})
	}
	medians := make(map[string]float64, len(methods))
	for _, method := range methods {
		medians[method] = median(samples[method])
	}
	return results, medians, nil
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
