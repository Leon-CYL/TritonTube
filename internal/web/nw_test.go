package web

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"tritontube/internal/proto"

	"google.golang.org/grpc"
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
			for range 10 {
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

type fakeStorageRPCClient struct {
	readResponse  *proto.BatchReadResponse
	readErr       error
	writeResponse *proto.BatchWriteResponse
	writeErr      error

	writeRequests []*proto.BatchWriteRequest
}

func (client *fakeStorageRPCClient) ListFiles(
	context.Context,
	*proto.BatchReadRequest,
	...grpc.CallOption,
) (*proto.BatchReadResponse, error) {
	return client.readResponse, client.readErr
}

func (client *fakeStorageRPCClient) ReadFile(
	_ context.Context,
	request *proto.ReadRequest,
	_ ...grpc.CallOption,
) (*proto.ReadResponse, error) {
	if client.readErr != nil {
		return nil, client.readErr
	}
	if client.readResponse == nil {
		return nil, nil
	}
	for _, entry := range client.readResponse.Entries {
		if entry.VideoId == request.VideoId && entry.Filename == request.Filename {
			return &proto.ReadResponse{Data: entry.Data}, nil
		}
	}
	return nil, fmt.Errorf("file not found: %s/%s", request.VideoId, request.Filename)
}

func (client *fakeStorageRPCClient) ReadFiles(
	_ context.Context,
	request *proto.BatchReadRequest,
	_ ...grpc.CallOption,
) (*proto.BatchReadResponse, error) {
	if client.readErr != nil || client.readResponse == nil || len(request.Requests) == 0 {
		return client.readResponse, client.readErr
	}
	entries := make([]*proto.FileEntry, 0, len(request.Requests))
	for _, requested := range request.Requests {
		for _, entry := range client.readResponse.Entries {
			if entry.VideoId == requested.VideoId && entry.Filename == requested.Filename {
				entries = append(entries, entry)
				break
			}
		}
	}
	return &proto.BatchReadResponse{Entries: entries}, nil
}

func (client *fakeStorageRPCClient) WriteFile(
	_ context.Context,
	request *proto.WriteRequest,
	_ ...grpc.CallOption,
) (*proto.WriteResponse, error) {
	client.writeRequests = append(client.writeRequests, &proto.BatchWriteRequest{Entries: []*proto.FileEntry{{
		VideoId: request.VideoId, Filename: request.Filename, Data: request.Data,
	}}})
	if client.writeErr != nil {
		return nil, client.writeErr
	}
	if client.writeResponse != nil && client.writeResponse.Cnt == 0 {
		return nil, errors.New("single-file write failed")
	}
	return &proto.WriteResponse{}, nil
}

func (client *fakeStorageRPCClient) WriteFiles(
	_ context.Context,
	request *proto.BatchWriteRequest,
	_ ...grpc.CallOption,
) (*proto.BatchWriteResponse, error) {
	client.writeRequests = append(client.writeRequests, request)
	if client.writeResponse != nil || client.writeErr != nil {
		return client.writeResponse, client.writeErr
	}
	return &proto.BatchWriteResponse{Cnt: uint32(len(request.Entries))}, nil
}

func configureMigrationFakes(
	t *testing.T,
	service *NetworkVideoContentService,
	clients map[string]*fakeStorageRPCClient,
) {
	t.Helper()

	service.dialStorageNode = func(address string) (storageRPCClient, func() error, error) {
		client, ok := clients[address]
		if !ok {
			return nil, nil, fmt.Errorf("no fake client for %s", address)
		}
		return client, func() error { return nil }, nil
	}
}

func TestWriteBatchGroupsFilesByOwnerAndBoundsRPCSize(t *testing.T) {
	service := NewNetworkVideoContentService(testStorageNodes)
	clients := make(map[string]*fakeStorageRPCClient, len(testStorageNodes))
	for _, address := range testStorageNodes {
		clients[address] = &fakeStorageRPCClient{}
	}
	configureMigrationFakes(t, service, clients)

	files := make([]ContentFile, 0, 25)
	for index := range 25 {
		files = append(files, ContentFile{
			VideoID:  "video",
			Filename: fmt.Sprintf("chunk-%05d.m4s", index),
			Data:     []byte{byte(index)},
		})
	}

	written, err := service.WriteBatch(files)
	if err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}
	if written != len(files) {
		t.Fatalf("WriteBatch count = %d, want %d", written, len(files))
	}
	for address, client := range clients {
		for _, request := range client.writeRequests {
			if len(request.Entries) > storageBatchSize {
				t.Fatalf("batch sent to %s contains %d files, limit %d", address, len(request.Entries), storageBatchSize)
			}
			for _, entry := range request.Entries {
				key := entry.VideoId + "/" + entry.Filename
				if owner := service.FindStorageAddr(key); owner != address {
					t.Fatalf("file %s sent to %s, owner is %s", key, address, owner)
				}
			}
		}
	}
}

func TestAddNodeFirstNode(t *testing.T) {
	service := NewNetworkVideoContentService(nil)
	dialed := ""
	service.dialStorageNode = func(address string) (storageRPCClient, func() error, error) {
		dialed = address
		return &fakeStorageRPCClient{}, func() error { return nil }, nil
	}

	const address = "node-a:9001"
	response, err := service.AddNode(t.Context(), &proto.AddNodeRequest{NodeAddress: address})
	if err != nil {
		t.Fatalf("AddNode failed: %v", err)
	}
	if response.MigratedFileCount != 0 {
		t.Fatalf("migrated count = %d, want 0", response.MigratedFileCount)
	}
	if dialed != address {
		t.Fatalf("verified node = %q, want %q", dialed, address)
	}
	if got := service.FindStorageAddr("video/manifest.mpd"); got != address {
		t.Fatalf("new ring owner = %q, want %q", got, address)
	}
}

func TestAddNodeRejectsDuplicate(t *testing.T) {
	const address = "node-a:9001"
	service := NewNetworkVideoContentService([]string{address})
	service.dialStorageNode = func(string) (storageRPCClient, func() error, error) {
		t.Fatal("duplicate node should not be dialed")
		return nil, nil, nil
	}

	_, err := service.AddNode(t.Context(), &proto.AddNodeRequest{NodeAddress: address})
	if err == nil {
		t.Fatal("AddNode expected duplicate-node error")
	}
}

func TestAddNodeCopiesOnlyReassignedFiles(t *testing.T) {
	const sourceAddress = "node-a:9001"
	const destinationAddress = "node-d:9004"

	entries := make([]*proto.FileEntry, 0, 100)
	for i := 0; i < 100; i++ {
		entries = append(entries, &proto.FileEntry{
			VideoId:  fmt.Sprintf("video-%d", i),
			Filename: "segment.m4s",
			Data:     []byte(fmt.Sprintf("data-%d", i)),
		})
	}

	source := &fakeStorageRPCClient{
		readResponse: &proto.BatchReadResponse{Entries: entries},
	}
	destination := &fakeStorageRPCClient{}
	service := NewNetworkVideoContentService([]string{sourceAddress})
	configureMigrationFakes(t, service, map[string]*fakeStorageRPCClient{
		sourceAddress:      source,
		destinationAddress: destination,
	})

	proposed := NewNetworkVideoContentService([]string{sourceAddress, destinationAddress})
	expected := make(map[string]bool)
	for _, entry := range entries {
		key := entry.VideoId + "/" + entry.Filename
		if proposed.FindStorageAddr(key) == destinationAddress {
			expected[key] = true
		}
	}
	if len(expected) == 0 {
		t.Fatal("test data produced no files assigned to the destination")
	}

	response, err := service.AddNode(t.Context(), &proto.AddNodeRequest{NodeAddress: destinationAddress})
	if err != nil {
		t.Fatalf("AddNode failed: %v", err)
	}
	if response.MigratedFileCount != int32(len(expected)) {
		t.Fatalf("migrated count = %d, want %d", response.MigratedFileCount, len(expected))
	}
	writtenRequests := 0
	for _, request := range destination.writeRequests {
		writtenRequests += len(request.Entries)
	}
	if writtenRequests != len(expected) {
		t.Fatalf("destination batch entries = %d, want %d", writtenRequests, len(expected))
	}

	for _, request := range destination.writeRequests {
		for _, entry := range request.Entries {
			key := entry.VideoId + "/" + entry.Filename
			if !expected[key] {
				t.Fatalf("AddNode copied file not assigned to destination: %s", key)
			}
			delete(expected, key)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("AddNode omitted %d reassigned files", len(expected))
	}
	if _, exists := service.storageServers[HashStringToUint64(destinationAddress)]; !exists {
		t.Fatal("destination was not published after successful migration")
	}
}

func TestAddNodeWithNoFilesToMove(t *testing.T) {
	const sourceAddress = "node-a:9001"
	const destinationAddress = "node-d:9004"

	source := &fakeStorageRPCClient{
		readResponse: &proto.BatchReadResponse{},
	}
	destination := &fakeStorageRPCClient{}
	service := NewNetworkVideoContentService([]string{sourceAddress})
	configureMigrationFakes(t, service, map[string]*fakeStorageRPCClient{
		sourceAddress:      source,
		destinationAddress: destination,
	})

	response, err := service.AddNode(t.Context(), &proto.AddNodeRequest{NodeAddress: destinationAddress})
	if err != nil {
		t.Fatalf("AddNode failed: %v", err)
	}
	if response.MigratedFileCount != 0 {
		t.Fatalf("migrated count = %d, want 0", response.MigratedFileCount)
	}
	if len(destination.writeRequests) != 0 {
		t.Fatalf("empty migration made %d WriteFile calls, want 0", len(destination.writeRequests))
	}
	if _, exists := service.storageServers[HashStringToUint64(destinationAddress)]; !exists {
		t.Fatal("destination was not published after empty migration")
	}
}

func TestAddNodeFailureKeepsOldRing(t *testing.T) {
	const sourceAddress = "node-a:9001"
	const destinationAddress = "node-d:9004"
	migrationErr := errors.New("migration failed")

	tests := []struct {
		name        string
		source      *fakeStorageRPCClient
		destination *fakeStorageRPCClient
	}{
		{
			name: "source read failure",
			source: &fakeStorageRPCClient{
				readErr: migrationErr,
			},
			destination: &fakeStorageRPCClient{},
		},
		{
			name: "destination write failure",
			source: &fakeStorageRPCClient{
				readResponse: &proto.BatchReadResponse{Entries: []*proto.FileEntry{
					migrationEntryForOwner(t, destinationAddress, []string{sourceAddress, destinationAddress}),
				}},
			},
			destination: &fakeStorageRPCClient{writeErr: migrationErr},
		},
		{
			name: "partial destination write",
			source: &fakeStorageRPCClient{
				readResponse: &proto.BatchReadResponse{Entries: []*proto.FileEntry{
					migrationEntryForOwner(t, destinationAddress, []string{sourceAddress, destinationAddress}),
				}},
			},
			destination: &fakeStorageRPCClient{
				writeResponse: &proto.BatchWriteResponse{Cnt: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewNetworkVideoContentService([]string{sourceAddress})
			oldIDs := append([]uint64(nil), service.storageIds...)
			oldServers := cloneStorageServers(service.storageServers)
			configureMigrationFakes(t, service, map[string]*fakeStorageRPCClient{
				sourceAddress:      tt.source,
				destinationAddress: tt.destination,
			})

			_, err := service.AddNode(t.Context(), &proto.AddNodeRequest{NodeAddress: destinationAddress})
			if err == nil {
				t.Fatal("AddNode expected migration error")
			}
			if !reflect.DeepEqual(service.storageIds, oldIDs) {
				t.Fatalf("storage IDs changed after failure: got %v want %v", service.storageIds, oldIDs)
			}
			if !reflect.DeepEqual(service.storageServers, oldServers) {
				t.Fatalf("storage servers changed after failure: got %v want %v", service.storageServers, oldServers)
			}
		})
	}
}

func TestRemoveNodeMigratesAllFilesBeforePublishingRing(t *testing.T) {
	const sourceAddress = "node-a:9001"
	const destinationAddress = "node-b:9002"

	entries := []*proto.FileEntry{
		{VideoId: "video-a", Filename: "manifest.mpd", Data: []byte("manifest")},
		{VideoId: "video-a", Filename: "chunk.m4s", Data: []byte("chunk")},
	}
	source := &fakeStorageRPCClient{
		readResponse: &proto.BatchReadResponse{Entries: entries},
	}
	destination := &fakeStorageRPCClient{}
	service := NewNetworkVideoContentService([]string{sourceAddress, destinationAddress})
	configureMigrationFakes(t, service, map[string]*fakeStorageRPCClient{
		sourceAddress:      source,
		destinationAddress: destination,
	})

	response, err := service.RemoveNode(t.Context(), &proto.RemoveNodeRequest{NodeAddress: sourceAddress})
	if err != nil {
		t.Fatalf("RemoveNode failed: %v", err)
	}
	if response.MigratedFileCount != int32(len(entries)) {
		t.Fatalf("migrated count = %d, want %d", response.MigratedFileCount, len(entries))
	}
	writtenRequests := 0
	for _, request := range destination.writeRequests {
		writtenRequests += len(request.Entries)
	}
	if writtenRequests != len(entries) {
		t.Fatalf("destination did not receive all files: %+v", destination.writeRequests)
	}
	if len(service.storageIds) != 1 || service.storageServers[service.storageIds[0]] != destinationAddress {
		t.Fatalf("ring after removal = %v / %v", service.storageIds, service.storageServers)
	}
}

func TestRemoveNodeFailureKeepsOldRing(t *testing.T) {
	const sourceAddress = "node-a:9001"
	const destinationAddress = "node-b:9002"

	migrationEntry := &proto.FileEntry{
		VideoId:  "video-a",
		Filename: "manifest.mpd",
		Data:     []byte("manifest"),
	}

	tests := []struct {
		name        string
		source      *fakeStorageRPCClient
		destination *fakeStorageRPCClient
	}{
		{
			name: "source read failure",
			source: &fakeStorageRPCClient{
				readErr: errors.New("read failed"),
			},
			destination: &fakeStorageRPCClient{},
		},
		{
			name: "destination write failure",
			source: &fakeStorageRPCClient{
				readResponse: &proto.BatchReadResponse{Entries: []*proto.FileEntry{migrationEntry}},
			},
			destination: &fakeStorageRPCClient{
				writeErr: errors.New("write failed"),
			},
		},
		{
			name: "partial destination write",
			source: &fakeStorageRPCClient{
				readResponse: &proto.BatchReadResponse{Entries: []*proto.FileEntry{migrationEntry}},
			},
			destination: &fakeStorageRPCClient{
				writeResponse: &proto.BatchWriteResponse{Cnt: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewNetworkVideoContentService([]string{sourceAddress, destinationAddress})
			oldIDs := append([]uint64(nil), service.storageIds...)
			oldServers := cloneStorageServers(service.storageServers)
			configureMigrationFakes(t, service, map[string]*fakeStorageRPCClient{
				sourceAddress:      tt.source,
				destinationAddress: tt.destination,
			})

			_, err := service.RemoveNode(t.Context(), &proto.RemoveNodeRequest{NodeAddress: sourceAddress})
			if err == nil {
				t.Fatal("RemoveNode expected migration error")
			}
			if !reflect.DeepEqual(service.storageIds, oldIDs) ||
				!reflect.DeepEqual(service.storageServers, oldServers) {
				t.Fatal("ring changed after failed removal")
			}
		})
	}
}

func TestRemoveEmptyNode(t *testing.T) {
	const sourceAddress = "node-a:9001"
	const destinationAddress = "node-b:9002"

	source := &fakeStorageRPCClient{
		readResponse: &proto.BatchReadResponse{},
	}
	destination := &fakeStorageRPCClient{}
	service := NewNetworkVideoContentService([]string{sourceAddress, destinationAddress})
	configureMigrationFakes(t, service, map[string]*fakeStorageRPCClient{
		sourceAddress:      source,
		destinationAddress: destination,
	})

	response, err := service.RemoveNode(t.Context(), &proto.RemoveNodeRequest{NodeAddress: sourceAddress})
	if err != nil {
		t.Fatalf("RemoveNode failed: %v", err)
	}
	if response.MigratedFileCount != 0 {
		t.Fatalf("migrated count = %d, want 0", response.MigratedFileCount)
	}
	if len(destination.writeRequests) != 0 {
		t.Fatalf("empty removal made %d WriteFile calls, want 0", len(destination.writeRequests))
	}
}

func TestAddNodeUnreachableDestinationKeepsOldRing(t *testing.T) {
	const sourceAddress = "node-a:9001"
	const destinationAddress = "node-d:9004"

	service := NewNetworkVideoContentService([]string{sourceAddress})
	oldIDs := append([]uint64(nil), service.storageIds...)
	oldServers := cloneStorageServers(service.storageServers)
	service.dialStorageNode = func(address string) (storageRPCClient, func() error, error) {
		if address == destinationAddress {
			return nil, nil, errors.New("connection refused")
		}
		return &fakeStorageRPCClient{}, func() error { return nil }, nil
	}

	_, err := service.AddNode(t.Context(), &proto.AddNodeRequest{NodeAddress: destinationAddress})
	if err == nil {
		t.Fatal("AddNode expected an unreachable-destination error")
	}
	if !reflect.DeepEqual(service.storageIds, oldIDs) ||
		!reflect.DeepEqual(service.storageServers, oldServers) {
		t.Fatal("ring changed after destination verification failed")
	}
}

func TestRemoveNodeValidation(t *testing.T) {
	t.Run("last node", func(t *testing.T) {
		service := NewNetworkVideoContentService([]string{"node-a:9001"})
		_, err := service.RemoveNode(t.Context(), &proto.RemoveNodeRequest{NodeAddress: "node-a:9001"})
		if err == nil {
			t.Fatal("RemoveNode expected last-node error")
		}
	})

	t.Run("unknown node", func(t *testing.T) {
		service := NewNetworkVideoContentService([]string{"node-a:9001", "node-b:9002"})
		_, err := service.RemoveNode(t.Context(), &proto.RemoveNodeRequest{NodeAddress: "unknown:9003"})
		if err == nil {
			t.Fatal("RemoveNode expected unknown-node error")
		}
	})
}

func migrationEntryForOwner(t *testing.T, owner string, nodes []string) *proto.FileEntry {
	t.Helper()

	ring := NewNetworkVideoContentService(nodes)
	for i := 0; i < 10_000; i++ {
		entry := &proto.FileEntry{
			VideoId:  fmt.Sprintf("migration-video-%d", i),
			Filename: "segment.m4s",
			Data:     []byte("data"),
		}
		if ring.FindStorageAddr(entry.VideoId+"/"+entry.Filename) == owner {
			return entry
		}
	}

	t.Fatalf("could not find a test key owned by %s", owner)
	return nil
}

func TestConcurrentMembershipReadsAndChanges(t *testing.T) {
	const initialNode = "node-a:9001"
	addedNodes := []string{
		"node-d:9004",
		"node-e:9005",
		"node-f:9006",
	}

	service := NewNetworkVideoContentService([]string{initialNode})
	clients := map[string]*fakeStorageRPCClient{
		initialNode: {readResponse: &proto.BatchReadResponse{}},
	}
	for _, address := range addedNodes {
		clients[address] = &fakeStorageRPCClient{
			readResponse: &proto.BatchReadResponse{},
		}
	}
	configureMigrationFakes(t, service, clients)

	stopReaders := make(chan struct{})
	var readers sync.WaitGroup
	for reader := 0; reader < 8; reader++ {
		readers.Add(1)
		go func(reader int) {
			defer readers.Done()

			for iteration := 0; ; iteration++ {
				select {
				case <-stopReaders:
					return
				default:
				}

				key := fmt.Sprintf("reader-%d/video-%d/manifest.mpd", reader, iteration)
				_ = service.FindStorageAddr(key)

				response, err := service.ListNodes(t.Context(), &proto.ListNodesRequest{})
				if err != nil {
					t.Errorf("ListNodes failed: %v", err)
					return
				}
				if len(response.Nodes) == 0 {
					t.Error("ListNodes returned no nodes during additions")
					return
				}
			}
		}(reader)
	}

	errs := make(chan error, len(addedNodes))
	var additions sync.WaitGroup
	for _, address := range addedNodes {
		address := address
		additions.Add(1)
		go func() {
			defer additions.Done()
			_, err := service.AddNode(t.Context(), &proto.AddNodeRequest{NodeAddress: address})
			errs <- err
		}()
	}

	additions.Wait()
	close(errs)

	_, removalErr := service.RemoveNode(
		t.Context(),
		&proto.RemoveNodeRequest{NodeAddress: addedNodes[0]},
	)

	close(stopReaders)
	readers.Wait()

	for err := range errs {
		if err != nil {
			t.Fatalf("AddNode failed: %v", err)
		}
	}
	if removalErr != nil {
		t.Fatalf("RemoveNode failed: %v", removalErr)
	}

	response, err := service.ListNodes(t.Context(), &proto.ListNodesRequest{})
	if err != nil {
		t.Fatalf("ListNodes failed: %v", err)
	}
	wantNodes := len(addedNodes)
	if len(response.Nodes) != wantNodes {
		t.Fatalf("ListNodes returned %d nodes, want %d", len(response.Nodes), wantNodes)
	}
}
