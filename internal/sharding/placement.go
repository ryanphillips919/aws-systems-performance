// Package sharding contains deterministic placement and a fan-out storage adapter.
package sharding

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

var validShardID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type ringNode struct {
	hash  uint64
	shard string
	vnode int
}
type Placement struct {
	Strategy   string   `json:"strategy"`
	IDs        []string `json:"shards"`
	VNodes     int      `json:"vnodes,omitempty"`
	MapVersion string   `json:"map_version,omitempty"`
	Partitions []string `json:"partitions,omitempty"`
	ring       []ringNode
}

func Hash(value string) uint64 {
	sum := sha256.Sum256([]byte(value))
	return binary.BigEndian.Uint64(sum[:8])
}
func NewPlacement(strategy string, ids []string, vnodes, partitions int, version string) (*Placement, error) {
	if len(ids) == 0 || len(ids) > 32 {
		return nil, fmt.Errorf("between 1 and 32 shards are required")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validShardID.MatchString(id) || seen[id] {
			return nil, fmt.Errorf("shard identifiers must be unique and contain 1–32 letters, digits, hyphens, or underscores")
		}
		seen[id] = true
	}
	p := &Placement{Strategy: strategy, IDs: append([]string(nil), ids...)}
	switch strategy {
	case "modulo":
	case "consistent":
		if vnodes < 1 || vnodes > 10000 {
			return nil, fmt.Errorf("virtual nodes must be between 1 and 10000")
		}
		p.VNodes = vnodes
		for _, id := range ids {
			for i := 0; i < vnodes; i++ {
				p.ring = append(p.ring, ringNode{hash: Hash(fmt.Sprintf("%s:%d", id, i)), shard: id, vnode: i})
			}
		}
		sort.Slice(p.ring, func(i, j int) bool {
			a, b := p.ring[i], p.ring[j]
			if a.hash != b.hash {
				return a.hash < b.hash
			}
			if a.shard != b.shard {
				return a.shard < b.shard
			}
			return a.vnode < b.vnode
		})
	case "partition":
		if partitions < 1 || partitions > 65536 || version == "" {
			return nil, fmt.Errorf("partition count and map version must be valid")
		}
		p.MapVersion = version
		p.Partitions = make([]string, partitions)
		for i := range p.Partitions {
			p.Partitions[i] = ids[i%len(ids)]
		}
	default:
		return nil, fmt.Errorf("unknown shard strategy %q (choose modulo, consistent, or partition)", strategy)
	}
	return p, nil
}
func (p *Placement) Owner(code string) string {
	h := Hash(code)
	switch p.Strategy {
	case "modulo":
		return p.IDs[h%uint64(len(p.IDs))]
	case "partition":
		return p.Partitions[h%uint64(len(p.Partitions))]
	default:
		i := sort.Search(len(p.ring), func(i int) bool { return p.ring[i].hash >= h })
		if i == len(p.ring) {
			i = 0
		}
		return p.ring[i].shard
	}
}
func (p *Placement) Fingerprint() string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
