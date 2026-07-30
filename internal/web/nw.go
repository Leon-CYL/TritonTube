// nw.go

package web

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"time"
	"tritontube/internal/proto"
	"tritontube/internal/storage"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// NetworkVideoContentService implements VideoContentService using a network of nodes.
type NetworkVideoContentService struct {
	proto.UnimplementedVideoContentAdminServiceServer
	storageIds      []uint64
	storageServers  map[uint64]string
	serverInstances map[string]*grpc.Server
	pendingWrites   map[string][]*proto.FileEntry
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
		storageIds:      storageIds,
		storageServers:  servers,
		serverInstances: make(map[string]*grpc.Server),
		pendingWrites:   make(map[string][]*proto.FileEntry),
	}
}

func HashStringToUint64(key string) uint64 {
	sum := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(sum[:8])
}

func (ns *NetworkVideoContentService) FindStorageAddr(str string) string {

	if len(ns.storageServers) == 0 {
		fmt.Printf("This server has 0 storage server available.\n")
		return ""
	}

	objId := HashStringToUint64(str)
	for _, id := range ns.storageIds {
		if objId <= id {
			return ns.storageServers[id]
		}
	}
	return ns.storageServers[ns.storageIds[0]]
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

// Admin code implementation

func (ns *NetworkVideoContentService) InitStorageServer(serverAddr string) error {
	baseDir := "./storage/" + serverAddr[len(serverAddr)-4:]

	// start the new node server

	grpcServer := grpc.NewServer()

	server := storage.NewStorageServer(baseDir, grpcServer)

	if server == nil {
		fmt.Printf("New Storage Server start failed\n")
		return errors.New("New Node server failed")
	}

	proto.RegisterVideoContentStorageServiceServer(grpcServer, server)

	lis, err := net.Listen("tcp", serverAddr)
	if err != nil {
		return err
	}

	ns.serverInstances[serverAddr] = grpcServer

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Printf("Server at %s stopped: %v\n", serverAddr, err)
		}
	}()

	return nil
}

func (ns *NetworkVideoContentService) AddNode(ctx context.Context, req *proto.AddNodeRequest) (*proto.AddNodeResponse, error) {
	newNodeId := HashStringToUint64(req.NodeAddress)
	if _, exists := ns.storageServers[newNodeId]; exists {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("storage node already exists: %s", req.NodeAddress)
	}

	peerAddr := ns.FindStorageAddr(req.NodeAddress)

	// Start new storage server
	if err := ns.InitStorageServer(req.NodeAddress); err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, err
	}

	// Update internal tracking before migrating files
	ns.storageServers[newNodeId] = req.NodeAddress
	ns.storageIds = append(ns.storageIds, newNodeId)
	sort.Slice(ns.storageIds, func(i, j int) bool { return ns.storageIds[i] < ns.storageIds[j] })

	// The first node has no existing peer to migrate files from.
	if peerAddr == "" {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, nil
	}

	// Init source client
	sourceConn, err := grpc.NewClient(
		peerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("connect to source node %s: %w", peerAddr, err)
	}
	defer sourceConn.Close()

	srcClient := proto.NewVideoContentStorageServiceClient(sourceConn)

	// Init destination client
	destinationConn, err := grpc.NewClient(
		req.NodeAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("connect to destination node %s: %w", req.NodeAddress, err)
	}
	defer destinationConn.Close()

	destClient := proto.NewVideoContentStorageServiceClient(destinationConn)

	// Transfer Files
	batchReadRes, err := srcClient.ReadFiles(ctx, &proto.BatchReadRequest{})
	if err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("read files from source node %s: %w", peerAddr, err)
	}
	if batchReadRes == nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, fmt.Errorf("source node %s returned an empty response", peerAddr)
	}
	fmt.Printf("Number of files in Node %v: %v\n", peerAddr, len(batchReadRes.Entries))

	count := 0
	batchWriteRequest := &proto.BatchWriteRequest{
		Entries: make([]*proto.FileEntry, 0),
	}

	for i := 0; i < len(batchReadRes.Entries); i++ {
		filePath := batchReadRes.Entries[i].VideoId + "/" + batchReadRes.Entries[i].Filename

		// Determine file assignment to new node base on location on the hash ring
		target := ns.FindStorageAddr(filePath)
		if target == req.NodeAddress {
			batchWriteRequest.Entries = append(batchWriteRequest.Entries, &proto.FileEntry{
				VideoId:  batchReadRes.Entries[i].VideoId,
				Filename: batchReadRes.Entries[i].Filename,
				Data:     batchReadRes.Entries[i].Data,
			})
			count++
		}
	}

	start := time.Now()

	writeResponse, err := destClient.WriteFiles(ctx, batchWriteRequest)
	if err != nil {
		log.Printf("Failed to send files: %v\n", err)
		return &proto.AddNodeResponse{MigratedFileCount: 0}, err
	}
	if writeResponse.Cnt != uint32(count) {
		return &proto.AddNodeResponse{MigratedFileCount: int32(writeResponse.Cnt)}, fmt.Errorf(
			"destination wrote %d of %d files",
			writeResponse.Cnt,
			count,
		)
	}

	end := time.Since(start)

	log.Printf("Added %d files to Node %s\n", count, req.NodeAddress)
	log.Printf("AddNode: Time taken to migrate files: %s\n", end)
	if count > 0 {
		log.Printf("AddNode: Average time per file: %s\n", end/time.Duration(count))
	}

	return &proto.AddNodeResponse{MigratedFileCount: int32(count)}, nil
}

func (ns *NetworkVideoContentService) RemoveNode(ctx context.Context, req *proto.RemoveNodeRequest) (*proto.RemoveNodeResponse, error) {
	if len(ns.storageIds) <= 1 {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, errors.New("cannot remove the last storage node")
	}

	removeNodeId := HashStringToUint64(req.NodeAddress)
	if _, exists := ns.storageServers[removeNodeId]; !exists {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, fmt.Errorf("storage node does not exist: %s", req.NodeAddress)
	}

	nodeId := ns.storageIds[0]

	for _, id := range ns.storageIds {
		if removeNodeId < id {
			nodeId = id
			break
		}
	}

	peerAddr := ns.storageServers[nodeId]

	// connect the source(removed) server
	sourceConn, err := grpc.NewClient(
		req.NodeAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	defer sourceConn.Close()

	srcClient := proto.NewVideoContentStorageServiceClient(sourceConn)

	// connect the destination server
	destinationConn, err := grpc.NewClient(
		peerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	defer destinationConn.Close()

	dstClient := proto.NewVideoContentStorageServiceClient(destinationConn)

	// Assign files from the removed server to the neighbor server based on consistant hashing
	response, err := srcClient.ReadFiles(ctx, &proto.BatchReadRequest{})
	if err != nil {
		log.Printf("ReadFile RPC failed: %v\n", err)
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}

	fmt.Printf("Number of files: %v\n", len(response.Entries))

	batchWriteRequest := &proto.BatchWriteRequest{
		Entries: response.Entries,
	}

	start := time.Now()

	writeResponse, err := dstClient.WriteFiles(ctx, batchWriteRequest)
	if err != nil {
		log.Printf("Failed to send files: %v\n", err)
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	if writeResponse.Cnt != uint32(len(response.Entries)) {
		return &proto.RemoveNodeResponse{MigratedFileCount: int32(writeResponse.Cnt)}, fmt.Errorf(
			"destination wrote %d of %d files",
			writeResponse.Cnt,
			len(response.Entries),
		)
	}

	end := time.Since(start)

	log.Printf("RemoveNode: Time taken to migrate files: %s\n", end)
	if len(response.Entries) > 0 {
		log.Printf("RemoveNode: Average time per file: %s\n", end/time.Duration(len(response.Entries)))
	}

	// shut down server
	if srv, ok := ns.serverInstances[req.NodeAddress]; ok {
		log.Printf("Gracefully stopping server at %s\n", req.NodeAddress)
		srv.GracefulStop()
	} else {
		// Fall back: issue a Shutdown RPC to remote server directly
		if _, err := srcClient.Shutdown(ctx, &proto.ShutdownRequest{}); err != nil {
			return &proto.RemoveNodeResponse{MigratedFileCount: int32(len(response.Entries))}, err
		}
	}

	for i, id := range ns.storageIds {
		if id == removeNodeId {
			ns.storageIds = append(ns.storageIds[:i], ns.storageIds[i+1:]...)
		}
	}
	delete(ns.storageServers, removeNodeId)
	delete(ns.serverInstances, req.NodeAddress)

	return &proto.RemoveNodeResponse{MigratedFileCount: int32(len(response.Entries))}, nil
}

func (ns *NetworkVideoContentService) ListNodes(ctx context.Context, req *proto.ListNodesRequest) (*proto.ListNodesResponse, error) {
	nodes := make([]string, 0, len(ns.storageIds))

	for _, id := range ns.storageIds {
		nodes = append(nodes, ns.storageServers[id])
	}

	return &proto.ListNodesResponse{Nodes: nodes}, nil
}
