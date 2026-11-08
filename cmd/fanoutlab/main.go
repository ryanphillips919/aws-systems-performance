// fanoutlab simulates independent shard completion events; it does not query databases.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
)

type point struct {
	Shards      int     `json:"shards"`
	Theoretical float64 `json:"theoretical"`
	Simulated   float64 `json:"simulated"`
}

func simulate(p float64, trials int, seed int64) []point {
	r := rand.New(rand.NewSource(seed))
	points := []point{}
	for _, n := range []int{1, 3, 5, 10, 20} {
		ok := 0
		for i := 0; i < trials; i++ {
			success := true
			for j := 0; j < n; j++ {
				if r.Float64() >= p {
					success = false
				}
			}
			if success {
				ok++
			}
		}
		points = append(points, point{n, math.Pow(p, float64(n)), float64(ok) / float64(trials)})
	}
	return points
}
func main() {
	p := flag.Float64("p", .99, "probability each shard meets threshold")
	trials := flag.Int("trials", 100000, "simulation trials")
	seed := flag.Int64("seed", 42, "random seed")
	flag.Parse()
	if math.IsNaN(*p) || *p < 0 || *p > 1 || *trials < 1 {
		fmt.Fprintln(os.Stderr, "p must be between 0 and 1 and trials positive")
		os.Exit(2)
	}
	result := struct {
		P      float64 `json:"p"`
		Trials int     `json:"trials"`
		Seed   int64   `json:"seed"`
		Note   string  `json:"note"`
		Points []point `json:"points"`
	}{*p, *trials, *seed, "independent simulated events; not measured PostgreSQL latency", simulate(*p, *trials, *seed)}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
