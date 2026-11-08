// shardlab measures logical placement only; it does not migrate database rows.
package main

import (
	"aws-systems-performance/internal/sharding"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
)

type Stats struct {
	Counts    map[string]int `json:"counts"`
	Min       int            `json:"min"`
	Max       int            `json:"max"`
	Average   float64        `json:"average"`
	Imbalance float64        `json:"max_over_average_minus_one"`
}
type Scenario struct {
	Strategy        string  `json:"strategy"`
	Change          string  `json:"change"`
	VNodes          int     `json:"vnodes,omitempty"`
	KeysMoved       int     `json:"keys_moved"`
	PercentMoved    float64 `json:"percent_moved"`
	Before          Stats   `json:"before"`
	After           Stats   `json:"after"`
	PartitionsMoved int     `json:"partitions_moved,omitempty"`
}

func stats(keys []string, p *sharding.Placement) Stats {
	counts := map[string]int{}
	for _, id := range p.IDs {
		counts[id] = 0
	}
	for _, key := range keys {
		counts[p.Owner(key)]++
	}
	s := Stats{Counts: counts, Min: len(keys), Average: float64(len(keys)) / float64(len(counts))}
	for _, v := range counts {
		if v < s.Min {
			s.Min = v
		}
		if v > s.Max {
			s.Max = v
		}
	}
	if s.Average > 0 {
		s.Imbalance = float64(s.Max)/s.Average - 1
	}
	return s
}
func compare(keys []string, before, after *sharding.Placement, change string) Scenario {
	n := 0
	for _, key := range keys {
		if before.Owner(key) != after.Owner(key) {
			n++
		}
	}
	return Scenario{Strategy: before.Strategy, Change: change, VNodes: before.VNodes, KeysMoved: n, PercentMoved: float64(n) * 100 / float64(len(keys)), Before: stats(keys, before), After: stats(keys, after)}
}
func must(strategy string, ids []string, vnodes, partitions int, version string) *sharding.Placement {
	p, err := sharding.NewPlacement(strategy, ids, vnodes, partitions, version)
	if err != nil {
		log.Fatal(err)
	}
	return p
}
func main() {
	count := flag.Int("keys", 100000, "number of keys")
	seed := flag.Int64("seed", 42, "random seed")
	partitions := flag.Int("partitions", 256, "logical partitions")
	flag.Parse()
	if *count < 1 || *count > 10000000 {
		log.Fatal("keys must be between 1 and 10000000")
	}
	r := rand.New(rand.NewSource(*seed))
	keys := make([]string, *count)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%016x-%d", r.Uint64(), i)
	}
	baseIDs := []string{"shard1", "shard2", "shard3"}
	added := []string{"shard1", "shard2", "shard3", "shard4"}
	removed := []string{"shard1", "shard2"}
	scenarios := []Scenario{}
	base := must("modulo", baseIDs, 128, *partitions, "v1")
	scenarios = append(scenarios, compare(keys, base, must("modulo", added, 128, *partitions, "v1"), "add shard4"), compare(keys, base, must("modulo", removed, 128, *partitions, "v1"), "remove shard3"))
	for _, vnodes := range []int{1, 16, 128, 512} {
		base = must("consistent", baseIDs, vnodes, *partitions, "v1")
		scenarios = append(scenarios, compare(keys, base, must("consistent", added, vnodes, *partitions, "v1"), "add shard4"), compare(keys, base, must("consistent", removed, vnodes, *partitions, "v1"), "remove shard3"))
	}
	base = must("partition", baseIDs, 128, *partitions, "v1")
	add := must("partition", added, 128, *partitions, "v2")
	remove := must("partition", removed, 128, *partitions, "v2")
	movedAdd, movedRemove := 0, 0
	for i, owner := range base.Partitions {
		add.Partitions[i] = owner
		if owner == "shard1" && i%4 == 0 {
			add.Partitions[i] = "shard4"
			movedAdd++
		}
		remove.Partitions[i] = owner
		if owner == "shard3" {
			remove.Partitions[i] = removed[i%len(removed)]
			movedRemove++
		}
	}
	a := compare(keys, base, add, "add shard4; move selected partitions")
	a.PartitionsMoved = movedAdd
	b := compare(keys, base, remove, "remove shard3; reassign its partitions")
	b.PartitionsMoved = movedRemove
	scenarios = append(scenarios, a, b)
	for _, s := range scenarios {
		fmt.Fprintf(os.Stderr, "%s %s (vnodes=%d): moved %d (%.2f%%), imbalance %.2f%%\n", s.Strategy, s.Change, s.VNodes, s.KeysMoved, s.PercentMoved, s.After.Imbalance*100)
		if math.IsNaN(s.PercentMoved) {
			log.Fatal("invalid result")
		}
	}
	output := struct {
		Keys       int        `json:"keys"`
		Seed       int64      `json:"seed"`
		Partitions int        `json:"partitions"`
		Note       string     `json:"note"`
		Scenarios  []Scenario `json:"scenarios"`
	}{*count, *seed, *partitions, "logical ownership only; no database rows are migrated", scenarios}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		log.Fatal(err)
	}
}
