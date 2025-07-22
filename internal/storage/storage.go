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

	// Initialize RocksDB
	opt := grocksdb.NewDefaultOptions()
	opt.SetCreateIfMissing(true)
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

	// Write file content to RocksDB, used videoId + filename as key and data as value
	wo := grocksdb.NewDefaultWriteOptions()
	defer wo.Destroy()
	err := ss.db.Put(wo, []byte(req.VideoId+"/"+req.Filename), req.Data)
	wo.SetSync(true)
	if err != nil {
		log.Printf("Storage: Write file failed: %v\n", err)
		return &proto.WriteResponse{}, err
	}

	return &proto.WriteResponse{}, nil
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

func (ss *StorageServer) ListFile(ctx context.Context, req *proto.ListRequest) (*proto.ListResponse, error) {
	videoIds := []string{}
	filenames := []string{}

	ro := grocksdb.NewDefaultReadOptions()
	it := ss.db.NewIterator(ro)
	defer it.Close()

	for it.SeekToFirst(); it.Valid(); it.Next() {
		key := string(it.Key().Data())
		parts := strings.SplitN(key, "/", 2)
		if len(parts) == 2 {
			videoIds = append(videoIds, parts[0])
			filenames = append(filenames, parts[1])
		}
		it.Key().Free()
	}
	fmt.Printf("ListFile: Found %d files\n", len(videoIds))

	return &proto.ListResponse{
		VideoIds:  videoIds,
		Filenames: filenames,
	}, nil
}

func (ss *StorageServer) SendFile(ctx context.Context, req *proto.SendRequest) (*proto.SendResponse, error) {
	conn, err := grpc.NewClient(req.PeerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("Failed to connect to server: %v", err)
		return &proto.SendResponse{}, err
	}
	defer conn.Close()

	client := proto.NewVideoContentStorageServiceClient(conn)

	_, err = client.WriteFile(context.Background(), &proto.WriteRequest{
		Data:     req.Data,
		VideoId:  req.VideoId,
		Filename: req.Filename,
	})

	if err != nil {
		log.Printf("WriteFile RPC failed: %v", err)
		return &proto.SendResponse{}, err
	}

	// Only delete from RocksDB if transfer is successful
	wo := grocksdb.NewDefaultWriteOptions()
	defer wo.Destroy()
	wo.SetSync(true)
	err = ss.db.Delete(wo, []byte(req.VideoId+"/"+req.Filename))
	if err != nil {
		log.Printf("Storage: Delete file failed: %v\n", err)
		return &proto.SendResponse{}, err
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
