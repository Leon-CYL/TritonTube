// nw.go

package web

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
	"tritontube/internal/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// NetworkVideoContentService implements VideoContentService using a network of nodes.
type NetworkVideoContentService struct {
	proto.UnimplementedVideoContentAdminServiceServer
	mu             sync.RWMutex
	membershipMu   sync.Mutex
	storageIds     []uint64
	storageServers map[uint64]string
	pendingWrites  map[string][]*proto.FileEntry

	dialStorageNode func(string) (storageRPCClient, func() error, error)
}

type storageRPCClient interface {
	ListFiles(context.Context, *proto.BatchReadRequest, ...grpc.CallOption) (*proto.BatchReadResponse, error)
	ReadFile(context.Context, *proto.ReadRequest, ...grpc.CallOption) (*proto.ReadResponse, error)
	WriteFile(context.Context, *proto.WriteRequest, ...grpc.CallOption) (*proto.WriteResponse, error)
	// Batch migration baseline disabled for Stage 3:
	// ReadFiles(context.Context, *proto.BatchReadRequest, ...grpc.CallOption) (*proto.BatchReadResponse, error)
	// WriteFiles(context.Context, *proto.BatchWriteRequest, ...grpc.CallOption) (*proto.BatchWriteResponse, error)
}

var _ VideoContentService = (*NetworkVideoContentService)(nil)

func NewNetworkVideoContentService(storageServers []string) *NetworkVideoContentService {

	storageIds := make([]uint64, 0, len(storageServers))
	servers := make(map[uint64]string, len(storageServers))
	for _, addr := range storageServers {
		id := HashStringToUint64(addr)
		storageIds = append(storageIds, id)
		servers[id] = addr
	}

	sort.Slice(storageIds, func(i int, j int) bool {
		return storageIds[i] < storageIds[j]
	})

	return &NetworkVideoContentService{
		storageIds:     storageIds,
		storageServers: servers,
		pendingWrites:  make(map[string][]*proto.FileEntry),
	}
}

func HashStringToUint64(key string) uint64 {
	sum := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(sum[:8])
}

func (ns *NetworkVideoContentService) FindStorageAddr(str string) string {
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	return findStorageAddr(str, ns.storageIds, ns.storageServers)
}

func (ns *NetworkVideoContentService) Read(videoId string, filename string) ([]byte, error) {
	filepath := videoId + "/" + filename

	storageAddr := ns.FindStorageAddr(filepath)
	if storageAddr == "" {
		return nil, fmt.Errorf("no valid storage address found for %s", filepath)
	}
	conn, err := grpc.NewClient(
		storageAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := proto.NewVideoContentStorageServiceClient(conn)

	response, err := client.ReadFile(context.Background(), &proto.ReadRequest{
		VideoId:  videoId,
		Filename: filename,
	})
	if err != nil {
		return nil, err
	}

	return response.Data, nil
}

func (ns *NetworkVideoContentService) Write(videoId string, filename string, data []byte) error {
	filepath := videoId + "/" + filename
	storageAddr := ns.FindStorageAddr(filepath)
	if storageAddr == "" {
		return fmt.Errorf("no valid storage address found for %s", filepath)
	}
	conn, err := grpc.NewClient(
		storageAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := proto.NewVideoContentStorageServiceClient(conn)

	req := &proto.WriteRequest{
		VideoId:  videoId,
		Filename: filename,
		Data:     data,
	}

	_, err = client.WriteFile(context.Background(), req)
	if err != nil {
		return err
	}

	return nil
}

func (ns *NetworkVideoContentService) AddNode(ctx context.Context, req *proto.AddNodeRequest) (*proto.AddNodeResponse, error) {
	ns.membershipMu.Lock()
	defer ns.membershipMu.Unlock()

	if req.NodeAddress == "" {
		return &proto.AddNodeResponse{}, errors.New("storage node address must not be empty")
	}

	newNodeId := HashStringToUint64(req.NodeAddress)
	ns.mu.RLock()
	_, exists := ns.storageServers[newNodeId]
	currentIDs := append([]uint64(nil), ns.storageIds...)
	currentServers := cloneStorageServers(ns.storageServers)
	ns.mu.RUnlock()

	if exists {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("storage node already exists: %s", req.NodeAddress)
	}

	peerAddr := findStorageAddr(req.NodeAddress, currentIDs, currentServers)
	proposedIDs := append([]uint64(nil), currentIDs...)
	proposedIDs = append(proposedIDs, newNodeId)
	sort.Slice(proposedIDs, func(i, j int) bool { return proposedIDs[i] < proposedIDs[j] })

	proposedServers := cloneStorageServers(currentServers)
	proposedServers[newNodeId] = req.NodeAddress

	// Storage processes are managed outside the web service. Verify that the
	// destination is already running before changing membership.
	destClient, closeDestination, err := ns.dialNode(ctx, req.NodeAddress)
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf(
			"connect to destination node %s: %w",
			req.NodeAddress,
			err,
		)
	}
	defer closeDestination()

	// The first node has no existing peer to migrate files from.
	if peerAddr == "" {
		ns.mu.Lock()
		ns.storageIds = proposedIDs
		ns.storageServers = proposedServers
		ns.mu.Unlock()
		return &proto.AddNodeResponse{MigratedFileCount: 0}, nil
	}

	srcClient, closeSource, err := ns.dialNode(ctx, peerAddr)
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("connect to source node %s: %w", peerAddr, err)
	}
	defer closeSource()

	listResponse, err := srcClient.ListFiles(ctx, &proto.BatchReadRequest{})
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("list files on source node %s: %w", peerAddr, err)
	}
	if listResponse == nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("source node %s returned an empty response", peerAddr)
	}
	fmt.Printf("Number of files in Node %v: %v\n", peerAddr, len(listResponse.Entries))

	filesToMigrate := make([]*proto.FileEntry, 0)
	for _, entry := range listResponse.Entries {
		filePath := entry.VideoId + "/" + entry.Filename

		target := findStorageAddr(filePath, proposedIDs, proposedServers)
		if target == req.NodeAddress {
			filesToMigrate = append(filesToMigrate, &proto.FileEntry{
				VideoId:  entry.VideoId,
				Filename: entry.Filename,
			})
		}
	}

	count := len(filesToMigrate)
	start := time.Now()

	// Batch ReadFiles/WriteFiles migration is intentionally disabled for the
	// Stage 3 sequential baseline. Transfer one file per RPC.
	written, err := migrateFilesSequential(ctx, srcClient, destClient, filesToMigrate)
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: int32(written)}, err
	}

	end := time.Since(start)

	ns.mu.Lock()
	ns.storageIds = proposedIDs
	ns.storageServers = proposedServers
	ns.mu.Unlock()
	log.Printf("Added %d files to Node %s\n", count, req.NodeAddress)
	log.Printf("AddNode: Time taken to migrate files: %s\n", end)
	if count > 0 {
		log.Printf("AddNode: Average time per file: %s\n", end/time.Duration(count))
	}

	return &proto.AddNodeResponse{MigratedFileCount: int32(count)}, nil
}

func (ns *NetworkVideoContentService) RemoveNode(ctx context.Context, req *proto.RemoveNodeRequest) (*proto.RemoveNodeResponse, error) {
	ns.membershipMu.Lock()
	defer ns.membershipMu.Unlock()

	ns.mu.RLock()
	currentIDs := append([]uint64(nil), ns.storageIds...)
	currentServers := cloneStorageServers(ns.storageServers)
	ns.mu.RUnlock()

	if len(currentIDs) <= 1 {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, errors.New("cannot remove the last storage node")
	}

	removeNodeId := HashStringToUint64(req.NodeAddress)
	if _, exists := currentServers[removeNodeId]; !exists {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, fmt.Errorf("storage node does not exist: %s", req.NodeAddress)
	}

	proposedIDs := make([]uint64, 0, len(currentIDs)-1)
	for _, id := range currentIDs {
		if id != removeNodeId {
			proposedIDs = append(proposedIDs, id)
		}
	}
	proposedServers := cloneStorageServers(currentServers)
	delete(proposedServers, removeNodeId)

	peerAddr := findStorageAddr(req.NodeAddress, proposedIDs, proposedServers)

	// connect the source(removed) server
	srcClient, closeSource, err := ns.dialNode(ctx, req.NodeAddress)
	if err != nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	defer closeSource()

	// connect the destination server
	dstClient, closeDestination, err := ns.dialNode(ctx, peerAddr)
	if err != nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	defer closeDestination()

	// Assign files from the removed server to the neighbor server based on consistant hashing
	response, err := srcClient.ListFiles(ctx, &proto.BatchReadRequest{})
	if err != nil {
		log.Printf("ListFiles RPC failed: %v\n", err)
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	if response == nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, errors.New("source node returned an empty response")
	}

	fmt.Printf("Number of files: %v\n", len(response.Entries))

	start := time.Now()

	// Batch ReadFiles/WriteFiles migration is intentionally disabled for the
	// Stage 3 sequential baseline. Transfer one file per RPC.
	written, err := migrateFilesSequential(ctx, srcClient, dstClient, response.Entries)
	if err != nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: int32(written)}, err
	}

	end := time.Since(start)

	log.Printf("RemoveNode: Time taken to migrate files: %s\n", end)
	if len(response.Entries) > 0 {
		log.Printf("RemoveNode: Average time per file: %s\n", end/time.Duration(len(response.Entries)))
	}

	ns.mu.Lock()
	ns.storageIds = proposedIDs
	ns.storageServers = proposedServers
	ns.mu.Unlock()

	return &proto.RemoveNodeResponse{MigratedFileCount: int32(len(response.Entries))}, nil
}

func migrateFilesSequential(
	ctx context.Context,
	source storageRPCClient,
	destination storageRPCClient,
	entries []*proto.FileEntry,
) (int, error) {
	for index, entry := range entries {
		readResponse, err := source.ReadFile(ctx, &proto.ReadRequest{
			VideoId:  entry.VideoId,
			Filename: entry.Filename,
		})
		if err != nil {
			return index, fmt.Errorf("read %s/%s: %w", entry.VideoId, entry.Filename, err)
		}
		if readResponse == nil {
			return index, fmt.Errorf("read %s/%s: empty response", entry.VideoId, entry.Filename)
		}

		_, err = destination.WriteFile(ctx, &proto.WriteRequest{
			VideoId:  entry.VideoId,
			Filename: entry.Filename,
			Data:     readResponse.Data,
		})
		if err != nil {
			return index, fmt.Errorf("write %s/%s: %w", entry.VideoId, entry.Filename, err)
		}
	}

	return len(entries), nil
}

/*
Stage 3 optimized migration reference (batch RPC version)

Keep these blocks commented while the sequential baseline is active. To restore
batch migration, re-enable ReadFiles/WriteFiles in storageRPCClient and replace
the ListFiles + migrateFilesSequential sections in AddNode and RemoveNode with
the corresponding blocks below.

AddNode batch read/filter/write:

	batchReadRes, err := srcClient.ReadFiles(ctx, &proto.BatchReadRequest{})
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("read files from source node %s: %w", peerAddr, err)
	}
	if batchReadRes == nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("source node %s returned an empty response", peerAddr)
	}
	fmt.Printf("Number of files in Node %v: %v\n", peerAddr, len(batchReadRes.Entries))

	batchWriteRequest := &proto.BatchWriteRequest{
		Entries: make([]*proto.FileEntry, 0),
	}
	for _, entry := range batchReadRes.Entries {
		filePath := entry.VideoId + "/" + entry.Filename
		target := findStorageAddr(filePath, proposedIDs, proposedServers)
		if target == req.NodeAddress {
			batchWriteRequest.Entries = append(batchWriteRequest.Entries, &proto.FileEntry{
				VideoId:  entry.VideoId,
				Filename: entry.Filename,
				Data:     entry.Data,
			})
		}
	}

	count := len(batchWriteRequest.Entries)
	start := time.Now()
	if count > 0 {
		writeResponse, err := destClient.WriteFiles(ctx, batchWriteRequest)
		if err != nil {
			log.Printf("Failed to send files: %v\n", err)
			return &proto.AddNodeResponse{MigratedFileCount: 0}, err
		}
		if writeResponse == nil || writeResponse.Cnt != uint32(count) {
			var written uint32
			if writeResponse != nil {
				written = writeResponse.Cnt
			}
			return &proto.AddNodeResponse{MigratedFileCount: int32(written)}, fmt.Errorf(
				"destination wrote %d of %d files", written, count,
			)
		}
	}

RemoveNode batch read/write:

	response, err := srcClient.ReadFiles(ctx, &proto.BatchReadRequest{})
	if err != nil {
		log.Printf("ReadFiles RPC failed: %v\n", err)
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	if response == nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, errors.New("source node returned an empty response")
	}
	fmt.Printf("Number of files: %v\n", len(response.Entries))

	batchWriteRequest := &proto.BatchWriteRequest{Entries: response.Entries}
	start := time.Now()
	if len(response.Entries) > 0 {
		writeResponse, err := dstClient.WriteFiles(ctx, batchWriteRequest)
		if err != nil {
			log.Printf("Failed to send files: %v\n", err)
			return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
		}
		if writeResponse == nil || writeResponse.Cnt != uint32(len(response.Entries)) {
			var written uint32
			if writeResponse != nil {
				written = writeResponse.Cnt
			}
			return &proto.RemoveNodeResponse{MigratedFileCount: int32(written)}, fmt.Errorf(
				"destination wrote %d of %d files", written, len(response.Entries),
			)
		}
	}
*/

func findStorageAddr(key string, storageIDs []uint64, storageServers map[uint64]string) string {
	if len(storageIDs) == 0 {
		return ""
	}

	objectID := HashStringToUint64(key)
	for _, id := range storageIDs {
		if objectID <= id {
			return storageServers[id]
		}
	}
	return storageServers[storageIDs[0]]
}

func cloneStorageServers(source map[uint64]string) map[uint64]string {
	result := make(map[uint64]string, len(source)+1)
	for id, address := range source {
		result[id] = address
	}
	return result
}

func (ns *NetworkVideoContentService) dialNode(ctx context.Context, address string) (storageRPCClient, func() error, error) {
	if ns.dialStorageNode != nil {
		return ns.dialStorageNode(address)
	}

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	conn.Connect()
	for state := conn.GetState(); state != connectivity.Ready; state = conn.GetState() {
		if !conn.WaitForStateChange(ctx, state) {
			conn.Close()
			return nil, nil, ctx.Err()
		}
	}
	return proto.NewVideoContentStorageServiceClient(conn), conn.Close, nil
}

func (ns *NetworkVideoContentService) ListNodes(ctx context.Context, req *proto.ListNodesRequest) (*proto.ListNodesResponse, error) {
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	nodes := make([]string, 0, len(ns.storageIds))

	for _, id := range ns.storageIds {
		nodes = append(nodes, ns.storageServers[id])
	}

	return &proto.ListNodesResponse{Nodes: nodes}, nil
}
