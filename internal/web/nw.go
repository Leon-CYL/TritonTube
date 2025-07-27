// Lab 8: Implement a network video content service (client using consistent hashing)

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
		log.Fatalf("No valid storage address found for: %s", filepath)
	}
	conn, err := grpc.NewClient(
		storageAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(64*1024*1024),
			grpc.MaxCallSendMsgSize(64*1024*1024),
		),
	)

	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
		return nil, err
	}
	defer conn.Close()

	client := proto.NewVideoContentStorageServiceClient(conn)

	response, err := client.ReadFile(context.Background(), &proto.ReadRequest{
		VideoId:  videoId,
		Filename: filename,
	})

	if err != nil {
		log.Fatalf("ReadFile RPC failed: %v", err)
		return nil, err
	}

	return response.Data, nil
}

func (ns *NetworkVideoContentService) Write(videoId string, filename string, data []byte) error {
	filepath := videoId + "/" + filename
	storageAddr := ns.FindStorageAddr(filepath)
	if storageAddr == "" {
		log.Fatalf("No valid storage address found for: %s", filepath)
	}
	conn, err := grpc.NewClient(
		storageAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(64*1024*1024),
			grpc.MaxCallSendMsgSize(64*1024*1024),
		),
	)
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
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
		log.Fatalf("WriteFile RPC failed: %v", err)
		return err
	}

	return nil
}

// Admin code implementation

func (ns *NetworkVideoContentService) InitStorageServer(serverAddr string) error {
	baseDir := "./storage/" + serverAddr[len(serverAddr)-4:]

	// start the new node server

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(64*1024*1024),
		grpc.MaxSendMsgSize(64*1024*1024),
	)

	server := storage.NewStorageServer(baseDir, grpcServer)

	if server == nil {
		fmt.Printf("New Storage Server start failed\n")
		return errors.New("New Node server failed")
	}

	proto.RegisterVideoContentStorageServiceServer(grpcServer, server)

	lis, err := net.Listen("tcp", serverAddr)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
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

	// Start new storage server
	if err := ns.InitStorageServer(req.NodeAddress); err != nil {
		return &proto.AddNodeResponse{MigratedFileCount: 0}, err
	}

	newNodeId := HashStringToUint64(req.NodeAddress)
	peerAddr := ns.FindStorageAddr(req.NodeAddress)

	// Update internal tracking before migrating files
	ns.storageServers[newNodeId] = req.NodeAddress
	ns.storageIds = append(ns.storageIds, newNodeId)
	sort.Slice(ns.storageIds, func(i, j int) bool { return ns.storageIds[i] < ns.storageIds[j] })

	conn, err := grpc.NewClient(
		peerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(64*1024*1024),
			grpc.MaxCallSendMsgSize(64*1024*1024),
		),
	)
	if err != nil {
		log.Printf("Failed to connect to node %s: %v", peerAddr, err)
	}
	defer conn.Close()

	client := proto.NewVideoContentStorageServiceClient(conn)

	batchReadRes, err := client.ReadFiles(context.Background(), &proto.BatchReadRequest{})
	if err != nil || batchReadRes == nil {
		log.Printf("Failed to list files from node %s: %v", peerAddr, err)
	}
	fmt.Printf("Number of files in Node %v: %v\n", peerAddr, len(batchReadRes.Entries))

	count := 0
	batchSendReq := &proto.BatchSendRequest{
		PeerAddr: req.NodeAddress,
		Entries:  make([]*proto.FileEntry, 0, len(batchReadRes.Entries)),
	}

	start := time.Now()
	for i := 0; i < len(batchReadRes.Entries); i++ {
		filePath := batchReadRes.Entries[i].VideoId + "/" + batchReadRes.Entries[i].Filename

		// Determine new node assignment
		target := ns.FindStorageAddr(filePath)
		if target == req.NodeAddress {

			batchSendReq.Entries = append(batchSendReq.Entries, &proto.FileEntry{
				VideoId:  batchReadRes.Entries[i].VideoId,
				Filename: batchReadRes.Entries[i].Filename,
				Data:     batchReadRes.Entries[i].Data,
			})

			count++
		}
	}

	_, err = client.SendFiles(context.Background(), batchSendReq)
	if err != nil {
		log.Printf("Failed to send files: %v\n", err)
		return &proto.AddNodeResponse{MigratedFileCount: int32(count)}, err
	}

	end := time.Since(start)
	log.Printf("Added %d files to Node %s\n", count, req.NodeAddress)

	log.Printf("AddNode: Time taken to migrate files: %s\n", end)
	log.Printf("AddNode: Average time per file: %s\n", end/time.Duration(count))

	return &proto.AddNodeResponse{MigratedFileCount: int32(count)}, nil
}

func (ns *NetworkVideoContentService) RemoveNode(ctx context.Context, req *proto.RemoveNodeRequest) (*proto.RemoveNodeResponse, error) {
	removeNodeId := HashStringToUint64(req.NodeAddress)
	nodeId := ns.storageIds[0]

	for _, id := range ns.storageIds {
		if removeNodeId < id {
			nodeId = id
			break
		}
	}

	peerAddr := ns.storageServers[nodeId]

	// connect the removed server
	conn, err := grpc.NewClient(
		req.NodeAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(64*1024*1024),
			grpc.MaxCallSendMsgSize(64*1024*1024),
		),
	)

	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}
	defer conn.Close()

	client := proto.NewVideoContentStorageServiceClient(conn)

	// Assign files from the removed server to the neighbor server based on consistant hashing
	response, err := client.ReadFiles(context.Background(), &proto.BatchReadRequest{})
	if err != nil {
		log.Printf("ListFile RPC failed: %v\n", err)
		return &proto.RemoveNodeResponse{MigratedFileCount: 0}, err
	}

	fmt.Printf("Number of files: %v\n", len(response.Entries))

	batchSendReq := &proto.BatchSendRequest{
		PeerAddr: peerAddr,
		Entries:  response.Entries,
	}

	start := time.Now()

	_, err = client.SendFiles(context.Background(), batchSendReq)
	if err != nil {
		log.Printf("Failed to send files: %v\n", err)
		return &proto.RemoveNodeResponse{MigratedFileCount: int32(len(response.Entries))}, err
	}

	end := time.Since(start)
	log.Printf("RemoveNode: Time taken to migrate files: %s\n", end)
	log.Printf("RemoveNode: Average time per file: %s\n", end/time.Duration(len(response.Entries)))

	for i, id := range ns.storageIds {
		if id == removeNodeId {
			ns.storageIds = append(ns.storageIds[:i], ns.storageIds[i+1:]...)
		}
	}
	delete(ns.storageServers, removeNodeId)

	// shut down server
	if srv, ok := ns.serverInstances[req.NodeAddress]; ok {
		log.Printf("Gracefully stopping server at %s\n", req.NodeAddress)
		srv.GracefulStop()
		delete(ns.serverInstances, req.NodeAddress)
	} else {
		// Fall back: issue a Shutdown RPC to remote server directly
		client.Shutdown(context.Background(), &proto.ShutdownRequest{})
	}

	return &proto.RemoveNodeResponse{MigratedFileCount: int32(len(response.Entries))}, nil
}

func (ns *NetworkVideoContentService) ListNodes(ctx context.Context, req *proto.ListNodesRequest) (*proto.ListNodesResponse, error) {
	nodes := make([]string, 0, len(ns.storageIds))

	for _, id := range ns.storageIds {
		nodes = append(nodes, ns.storageServers[id])
	}

	return &proto.ListNodesResponse{Nodes: nodes}, nil
}
