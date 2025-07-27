// Lab 8: Implement a network video content service (server)

package storage

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"tritontube/internal/proto"

	grocksdb "github.com/linxGnu/grocksdb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Implement a network video content service (server)
type StorageServer struct {
	proto.UnimplementedVideoContentStorageServiceServer
	basePath   string
	grpcServer *grpc.Server
	db         *grocksdb.DB
}

func NewStorageServer(base string, server *grpc.Server) *StorageServer {
	if err := os.MkdirAll(base, os.ModePerm); err != nil {
		fmt.Printf("Failed to create storage directory: %v\n", err)
		return nil
	}

	// Initialize and configure RocksDB
	opt := grocksdb.NewDefaultOptions()
	opt.SetCreateIfMissing(true)

	// 4 background threads for RocksDB
	opt.IncreaseParallelism(4)

	// I/O optimization: Sync every 1MB instead of every write to reduce disk I/O
	opt.SetUseFsync(false)
	opt.SetBytesPerSync(1 * 1024 * 1024)

	// Set up block-based table options (for Bloom filters and caching): 10 bits per key Bloom filter and 10MB LRU block cache
	blockOpts := grocksdb.NewDefaultBlockBasedTableOptions()
	blockOpts.SetFilterPolicy(grocksdb.NewBloomFilter(10))
	blockOpts.SetBlockCache(grocksdb.NewLRUCache(10 * 1024 * 1024))
	opt.SetBlockBasedTableFactory(blockOpts)

	// --- Open DB ---
	db, err := grocksdb.OpenDb(opt, base)
	if err != nil {
		fmt.Printf("Storage Server Start Error: %v\n", err)
		return nil
	}

	return &StorageServer{
		basePath:   base,
		grpcServer: server,
		db:         db,
	}
}

func (ss *StorageServer) WriteFile(ctx context.Context, req *proto.WriteRequest) (*proto.WriteResponse, error) {
		wo := grocksdb.NewDefaultWriteOptions()
		defer wo.Destroy()

		err := ss.db.Put(wo, []byte(req.VideoId+"/"+req.Filename), req.Data)
		if err != nil {
			log.Printf("Storage: Write file failed: %v\n", err)
			return &proto.WriteResponse{}, err
		}

		return &proto.WriteResponse{}, nil
}

func (ss *StorageServer) WriteFiles(ctx context.Context, req *proto.BatchWriteRequest) (*proto.BatchWriteResponse, error) {
	writeBatch := grocksdb.NewWriteBatch()
	defer writeBatch.Destroy()

	for _, entry := range req.Entries {
		key := []byte(entry.VideoId + "/" + entry.Filename)
		writeBatch.Put(key, entry.Data)
	}

	wo := grocksdb.NewDefaultWriteOptions()
	wo.SetSync(true)
	defer wo.Destroy()

	err := ss.db.Write(wo, writeBatch)
	if err != nil {
		log.Printf("Storage: Batch write failed: %v\n", err)
		return &proto.BatchWriteResponse{}, err
	}

	return &proto.BatchWriteResponse{}, nil
}

func (ss *StorageServer) ReadFile(ctx context.Context, req *proto.ReadRequest) (*proto.ReadResponse, error) {
	// Read file content from RocksDB, used videoId + filename as key and data as value
	ro := grocksdb.NewDefaultReadOptions()
	defer ro.Destroy()
	value, err := ss.db.Get(ro, []byte(req.VideoId+"/"+req.Filename))
	data := append([]byte{}, value.Data()...)
	defer value.Free()
	if err != nil {
		log.Printf("Storage: Read file failed: %v\n", err)
		return &proto.ReadResponse{Data: nil}, err
	}
	return &proto.ReadResponse{Data: data}, nil
}

func (ss *StorageServer) ReadFiles(ctx context.Context, req *proto.BatchReadRequest) (*proto.BatchReadResponse, error) {

	ro := grocksdb.NewDefaultReadOptions()
	it := ss.db.NewIterator(ro)
	defer it.Close()

	entries := make([]*proto.FileEntry, 0)

	for it.SeekToFirst(); it.Valid(); it.Next() {
		key := string(it.Key().Data())
		parts := strings.SplitN(key, "/", 2)
		if len(parts) == 2 {
			value := append([]byte{}, it.Value().Data()...)

			entry := &proto.FileEntry{
				VideoId:  parts[0],
				Filename: parts[1],
				Data:     value,
			}
			entries = append(entries, entry)
		}
		it.Key().Free()
	}
	fmt.Printf("ListFile: Found %d files\n", len(entries))

	return &proto.BatchReadResponse{
		Entries: entries,
	}, nil
}

func (ss *StorageServer) SendFiles(ctx context.Context, req *proto.BatchSendRequest) (*proto.SendResponse, error) {
	conn, err := grpc.NewClient(req.PeerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("Failed to connect to server: %v", err)
		return &proto.SendResponse{}, err
	}
	defer conn.Close()

	client := proto.NewVideoContentStorageServiceClient(conn)

	// Build the whole batch once
	writeReq := &proto.BatchWriteRequest{
		Entries: make([]*proto.FileEntry, 0, len(req.Entries)),
	}

	for _, entry := range req.Entries {
		writeReq.Entries = append(writeReq.Entries, &proto.FileEntry{
			VideoId:  entry.VideoId,
			Filename: entry.Filename,
			Data:     entry.Data,
		})
	}

	// Send once
	_, err = client.WriteFiles(context.Background(), writeReq)
	if err != nil {
		log.Printf("WriteFiles RPC failed: %v", err)
		return &proto.SendResponse{}, err
	}

	// Delete only after successful transfer
	for _, entry := range req.Entries {
		wo := grocksdb.NewDefaultWriteOptions()
		wo.SetSync(false)
		err = ss.db.Delete(wo, []byte(entry.VideoId+"/"+entry.Filename))
		wo.Destroy()
		if err != nil {
			log.Printf("Delete failed: %v", err)
			return &proto.SendResponse{}, err
		}
	}
	
	return &proto.SendResponse{}, nil
}

func (ss *StorageServer) Shutdown(ctx context.Context, req *proto.ShutdownRequest) (*proto.ShutdownResponse, error) {
	fmt.Println("Received shutdown request. Stopping server...")
	ss.db.Close()
	go func() {
		time.Sleep(500 * time.Millisecond)
		ss.grpcServer.GracefulStop()
	}()
	return &proto.ShutdownResponse{}, nil
}
