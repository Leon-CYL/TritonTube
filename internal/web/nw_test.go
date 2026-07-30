package web

import (
	"fmt"
	"testing"
)

var testStorageNodes = []string{
	"node-a:9001",
	"node-b:9002",
	"node-c:9003",
}

func TestHashStringToUint64(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want uint64
	}{
		{
			name: "storage node",
			key:  "node-a:9001",
			want: 12545143070306242735,
		},
		{
			name: "manifest",
			key:  "video-a/manifest.mpd",
			want: 5268350921027833927,
		},
		{
			name: "segment",
			key:  "video-b/chunk-00001.m4s",
			want: 4711868119580278890,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HashStringToUint64(tt.key); got != tt.want {
				t.Fatalf("HashStringToUint64(%q) = %d, want %d", tt.key, got, tt.want)
			}
		})
	}
}

func TestHashRingDeterministicPlacement(t *testing.T) {
	ring := NewNetworkVideoContentService(testStorageNodes)

	tests := []struct {
		key  string
		want string
	}{
		{key: "video-0/segment.m4s", want: "node-a:9001"},
		{key: "video-1/segment.m4s", want: "node-c:9003"},
		{key: "video-9/segment.m4s", want: "node-b:9002"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			for i := 0; i < 10; i++ {
				if got := ring.FindStorageAddr(tt.key); got != tt.want {
					t.Fatalf("FindStorageAddr(%q) = %q, want %q", tt.key, got, tt.want)
				}
			}
		})
	}
}

func TestHashRingPlacementDoesNotDependOnInputOrder(t *testing.T) {
	first := NewNetworkVideoContentService([]string{
		"node-a:9001",
		"node-b:9002",
		"node-c:9003",
	})
	second := NewNetworkVideoContentService([]string{
		"node-c:9003",
		"node-a:9001",
		"node-b:9002",
	})

	for i := 0; i < 10_000; i++ {
		key := testKey(i)
		firstOwner := first.FindStorageAddr(key)
		secondOwner := second.FindStorageAddr(key)
		if firstOwner != secondOwner {
			t.Fatalf(
				"placement for %q depends on constructor order: first=%q second=%q",
				key,
				firstOwner,
				secondOwner,
			)
		}
	}
}

func TestHashRingWraparound(t *testing.T) {
	ring := NewNetworkVideoContentService(testStorageNodes)
	key := "video-1/segment.m4s"

	largestNodeID := ring.storageIds[len(ring.storageIds)-1]
	if keyID := HashStringToUint64(key); keyID <= largestNodeID {
		t.Fatalf("test key hash %d must be larger than largest node hash %d", keyID, largestNodeID)
	}

	firstNode := ring.storageServers[ring.storageIds[0]]
	if got := ring.FindStorageAddr(key); got != firstNode {
		t.Fatalf("wraparound owner = %q, want first ring node %q", got, firstNode)
	}
}

func TestHashRingEmpty(t *testing.T) {
	ring := NewNetworkVideoContentService(nil)

	if got := ring.FindStorageAddr("video/manifest.mpd"); got != "" {
		t.Fatalf("empty ring returned %q, want empty address", got)
	}
}

func TestHashRingAddingNodeMovesOnlyKeysOwnedByNewNode(t *testing.T) {
	const keyCount = 10_000
	const newNode = "node-d:9004"

	before := NewNetworkVideoContentService(testStorageNodes)
	after := NewNetworkVideoContentService(appendCopy(testStorageNodes, newNode))

	moved := 0
	for i := 0; i < keyCount; i++ {
		key := testKey(i)
		oldOwner := before.FindStorageAddr(key)
		newOwner := after.FindStorageAddr(key)
		if oldOwner == newOwner {
			continue
		}

		moved++
		if newOwner != newNode {
			t.Fatalf(
				"adding %q moved key %q from %q to unrelated node %q",
				newNode,
				key,
				oldOwner,
				newOwner,
			)
		}
	}

	if moved == 0 || moved == keyCount {
		t.Fatalf("adding one node moved %d/%d keys; want a non-zero proper subset", moved, keyCount)
	}

	t.Logf("adding %s moved %d/%d keys (%.2f%%)", newNode, moved, keyCount, percentage(moved, keyCount))
}

func TestHashRingRemovingNodeMovesOnlyItsKeys(t *testing.T) {
	const keyCount = 10_000
	const removedNode = "node-b:9002"

	before := NewNetworkVideoContentService(testStorageNodes)
	after := NewNetworkVideoContentService([]string{
		"node-a:9001",
		"node-c:9003",
	})

	moved := 0
	for i := 0; i < keyCount; i++ {
		key := testKey(i)
		oldOwner := before.FindStorageAddr(key)
		newOwner := after.FindStorageAddr(key)
		if oldOwner == newOwner {
			continue
		}

		moved++
		if oldOwner != removedNode {
			t.Fatalf(
				"removing %q moved key %q previously owned by %q to %q",
				removedNode,
				key,
				oldOwner,
				newOwner,
			)
		}
	}

	if moved == 0 || moved == keyCount {
		t.Fatalf("removing one node moved %d/%d keys; want a non-zero proper subset", moved, keyCount)
	}

	t.Logf("removing %s moved %d/%d keys (%.2f%%)", removedNode, moved, keyCount, percentage(moved, keyCount))
}

func TestHashRingDistributionAcrossNodes(t *testing.T) {
	const keyCount = 30_000

	ring := NewNetworkVideoContentService(testStorageNodes)
	counts := make(map[string]int, len(testStorageNodes))

	for i := 0; i < keyCount; i++ {
		counts[ring.FindStorageAddr(testKey(i))]++
	}

	total := 0
	for _, node := range testStorageNodes {
		count := counts[node]
		total += count

		share := percentage(count, keyCount)
		t.Logf("%s owns %d/%d keys (%.2f%%)", node, count, keyCount, share)

		// The current ring intentionally has one point per node, so this is a
		// broad smoke-test threshold rather than a strict balance guarantee.
		if share < 5 || share > 60 {
			t.Errorf("%s owns %.2f%% of keys, outside expected 5%%-60%% range", node, share)
		}
	}

	if total != keyCount {
		t.Fatalf("distribution accounted for %d keys, want %d", total, keyCount)
	}
}

func testKey(index int) string {
	return fmt.Sprintf("video-%05d/chunk-%05d.m4s", index/10, index)
}

func appendCopy(values []string, value string) []string {
	result := append([]string(nil), values...)
	return append(result, value)
}

func percentage(part, total int) float64 {
	return float64(part) * 100 / float64(total)
}
