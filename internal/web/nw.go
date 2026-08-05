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
	ReadFiles(context.Context, *proto.BatchReadRequest, ...grpc.CallOption) (*proto.BatchReadResponse, error)
	WriteFiles(context.Context, *proto.BatchWriteRequest, ...grpc.CallOption) (*proto.BatchWriteResponse, error)
}

const storageBatchSize = 4

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

	start := time.Now()
	storageAddr := ns.FindStorageAddr(filepath)
	if storageAddr == "" {
		return nil, fmt.Errorf("no valid storage address found for %s", filepath)
	}
	hashLookupTime := time.Since(start)
	log.Printf("Consistent hash lookup time: %.3f ms", durationMilliseconds(hashLookupTime))

	conn, err := grpc.NewClient(
		storageAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(proto.MaxMessageSize),
			grpc.MaxCallSendMsgSize(proto.MaxMessageSize),
		),
	)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := proto.NewVideoContentStorageServiceClient(conn)

	start = time.Now()
	response, err := client.ReadFile(context.Background(), &proto.ReadRequest{
		VideoId:  videoId,
		Filename: filename,
	})
	if err != nil {
		return nil, err
	}
	grpcTime := time.Since(start)
	log.Printf("gRPC read file time: %.3f ms", durationMilliseconds(grpcTime))

	return response.Data, nil
}

func (ns *NetworkVideoContentService) Write(videoId string, filename string, data []byte) error {
	count, err := ns.WriteBatch([]ContentFile{{VideoID: videoId, Filename: filename, Data: data}})
	if err == nil && count != 1 {
		return fmt.Errorf("wrote %d of 1 file", count)
	}
	return err
}

func (ns *NetworkVideoContentService) WriteBatch(files []ContentFile) (int, error) {
	grouped := make(map[string][]*proto.FileEntry)
	for _, file := range files {
		key := file.VideoID + "/" + file.Filename
		storageAddr := ns.FindStorageAddr(key)
		if storageAddr == "" {
			return 0, fmt.Errorf("no valid storage address found for %s", key)
		}
		grouped[storageAddr] = append(grouped[storageAddr], &proto.FileEntry{
			VideoId: file.VideoID, Filename: file.Filename, Data: file.Data,
		})
	}

	written := 0
	for storageAddr, entries := range grouped {
		client, closeClient, err := ns.dialNode(context.Background(), storageAddr)
		if err != nil {
			return written, fmt.Errorf("connect to storage node %s: %w", storageAddr, err)
		}
		for start := 0; start < len(entries); start += storageBatchSize {
			end := min(start+storageBatchSize, len(entries))
			response, writeErr := client.WriteFiles(context.Background(), &proto.BatchWriteRequest{Entries: entries[start:end]})
			if writeErr != nil {
				closeClient()
				return written, fmt.Errorf("batch write to %s: %w", storageAddr, writeErr)
			}
			if response == nil || response.Cnt != uint32(end-start) {
				closeClient()
				return written, fmt.Errorf("batch write to %s wrote %d of %d files", storageAddr, response.GetCnt(), end-start)
			}
			written += int(response.Cnt)
		}
		if err := closeClient(); err != nil {
			return written, err
		}
	}
	return written, nil
}

func (ns *NetworkVideoContentService) AddNode(ctx context.Context, req *proto.AddNodeRequest) (*proto.AddNodeResponse, error) {
	operationStart := time.Now()
	defer func() {
		log.Printf("AddNode total time: %.3f ms", durationMilliseconds(time.Since(operationStart)))
	}()

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

	start := time.Now()
	peerAddr := findStorageAddr(req.NodeAddress, currentIDs, currentServers)
	peerTime := time.Since(start)
	log.Printf("Peer look up time: %.3f ms\n", durationMilliseconds(peerTime))

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

	start = time.Now()
	listResponse, err := srcClient.ListFiles(ctx, &proto.BatchReadRequest{})
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("list files on source node %s: %w", peerAddr, err)
	}
	if listResponse == nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("source node %s returned an empty response", peerAddr)
	}
	listFilesTime := time.Since(start)
	log.Printf("AddNode ListFiles time: %.3f ms", durationMilliseconds(listFilesTime))
	log.Printf("Number of files in Node %v: %v\n", peerAddr, len(listResponse.Entries))

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
	start = time.Now()

	written, readTime, writeTime, err := migrateFilesBatch(ctx, srcClient, destClient, filesToMigrate)
	log.Printf("AddNode batch ReadFiles time: %.3f ms", durationMilliseconds(readTime))
	log.Printf("AddNode batch WriteFiles time: %.3f ms", durationMilliseconds(writeTime))
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: int32(written)}, err
	}

	end := time.Since(start)

	ns.mu.Lock()
	ns.storageIds = proposedIDs
	ns.storageServers = proposedServers
	ns.mu.Unlock()
	log.Printf("Added %d files to Node %s\n", count, req.NodeAddress)
	log.Printf("AddNode: Time taken to migrate files: %.3f ms", durationMilliseconds(end))
	if count > 0 {
		log.Printf("AddNode: Average time per file: %.3f ms", durationMilliseconds(end)/float64(count))
	}

	return &proto.AddNodeResponse{MigratedFileCount: int32(count)}, nil
}

func (ns *NetworkVideoContentService) RemoveNode(ctx context.Context, req *proto.RemoveNodeRequest) (*proto.RemoveNodeResponse, error) {
	operationStart := time.Now()
	defer func() {
		log.Printf("RemoveNode total time: %.3f ms", durationMilliseconds(time.Since(operationStart)))
	}()

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

	start := time.Now()
	peerAddr := findStorageAddr(req.NodeAddress, proposedIDs, proposedServers)
	peerTime := time.Since(start)
	log.Printf("Peer look up time: %.3f ms\n", durationMilliseconds(peerTime))

	srcClient, closeSource, err := ns.dialNode(ctx, req.NodeAddress)
	if err != nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	defer closeSource()

	dstClient, closeDestination, err := ns.dialNode(ctx, peerAddr)
	if err != nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	defer closeDestination()

	start = time.Now()
	response, err := srcClient.ListFiles(ctx, &proto.BatchReadRequest{})
	if err != nil {
		log.Printf("ListFiles RPC failed: %v\n", err)
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	if response == nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, errors.New("source node returned an empty response")
	}
	listFilesTime := time.Since(start)
	log.Printf("RemoveNode ListFiles time: %.3f ms", durationMilliseconds(listFilesTime))
	log.Printf("Number of files: %v\n", len(response.Entries))

	start = time.Now()
	written, readTime, writeTime, err := migrateFilesBatch(ctx, srcClient, dstClient, response.Entries)
	log.Printf("RemoveNode batch ReadFiles time: %.3f ms", durationMilliseconds(readTime))
	log.Printf("RemoveNode batch WriteFiles time: %.3f ms", durationMilliseconds(writeTime))
	if err != nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: int32(written)}, err
	}
	end := time.Since(start)

	log.Printf("RemoveNode: Time taken to migrate files: %.3f ms\n", durationMilliseconds(end))
	if len(response.Entries) > 0 {
		log.Printf(
			"RemoveNode: Average time per file: %.3f ms",
			durationMilliseconds(end)/float64(len(response.Entries)),
		)
	}

	ns.mu.Lock()
	ns.storageIds = proposedIDs
	ns.storageServers = proposedServers
	ns.mu.Unlock()

	return &proto.RemoveNodeResponse{MigratedFileCount: int32(len(response.Entries))}, nil
}

func migrateFilesBatch(
	ctx context.Context,
	source storageRPCClient,
	destination storageRPCClient,
	entries []*proto.FileEntry,
) (int, time.Duration, time.Duration, error) {
	var totalReadTime time.Duration
	var totalWriteTime time.Duration
	written := 0

	for start := 0; start < len(entries); start += storageBatchSize {
		end := min(start+storageBatchSize, len(entries))
		requests := make([]*proto.ReadRequest, 0, end-start)
		for _, entry := range entries[start:end] {
			requests = append(requests, &proto.ReadRequest{VideoId: entry.VideoId, Filename: entry.Filename})
		}

		readStart := time.Now()
		readResponse, err := source.ReadFiles(ctx, &proto.BatchReadRequest{Requests: requests})
		totalReadTime += time.Since(readStart)
		if err != nil {
			return written, totalReadTime, totalWriteTime, fmt.Errorf("read batch starting at file %d: %w", start, err)
		}
		if readResponse == nil || len(readResponse.Entries) != end-start {
			return written, totalReadTime, totalWriteTime, fmt.Errorf("read batch returned %d of %d files", len(readResponse.GetEntries()), end-start)
		}

		writeStart := time.Now()
		writeResponse, err := destination.WriteFiles(ctx, &proto.BatchWriteRequest{Entries: readResponse.Entries})
		totalWriteTime += time.Since(writeStart)
		if err != nil {
			return written, totalReadTime, totalWriteTime, fmt.Errorf("write batch starting at file %d: %w", start, err)
		}
		if writeResponse == nil || writeResponse.Cnt != uint32(end-start) {
			return written, totalReadTime, totalWriteTime, fmt.Errorf("write batch wrote %d of %d files", writeResponse.GetCnt(), end-start)
		}
		written += int(writeResponse.Cnt)
	}

	return written, totalReadTime, totalWriteTime, nil
}

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

	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(proto.MaxMessageSize),
			grpc.MaxCallSendMsgSize(proto.MaxMessageSize),
		),
	)
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
