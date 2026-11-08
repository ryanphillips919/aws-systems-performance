package sharding

import (
	"fmt"
	"strconv"
	"strings"
)

func ConfigFromEnv(getenv func(string) string) (*Placement, []string, error) {
	value := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	ids := strings.Split(value("SHARD_IDS", "shard1,shard2,shard3"), ",")
	vnodes, err := strconv.Atoi(value("SHARD_VNODES", "128"))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid SHARD_VNODES: %w", err)
	}
	partitions, err := strconv.Atoi(value("SHARD_PARTITIONS", "256"))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid SHARD_PARTITIONS: %w", err)
	}
	p, err := NewPlacement(value("SHARD_STRATEGY", "consistent"), ids, vnodes, partitions, value("SHARD_MAP_VERSION", "v1"))
	if err != nil {
		return nil, nil, err
	}
	urls := strings.Split(getenv("SHARD_DATABASE_URLS"), ",")
	if len(urls) != len(ids) {
		return nil, nil, fmt.Errorf("SHARD_DATABASE_URLS must contain one URL per shard ID")
	}
	return p, urls, nil
}
