// Lab 8: Implement a network video content service (server)

package storage

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
	"tritontube/internal/proto"

	"github.com/tecbot/gorocksdb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Implement a network video content service (server)
type StorageServer struct {
	proto.UnimplementedVideoContentStorageServiceServer
	basePath   string
	videoids   []string
	filenames  []string
	grpcServer *grpc.Server
	db         *gorocksdb.DB
}

func NewStorageServer(base string, server *grpc.Server) *StorageServer {
	opt := gorocksdb.NewDefaultOptions()
	opt.SetCreateIfMissing(true)
	db, err := gorocksdb.OpenDb(opt, base)
	if err != nil {
		fmt.Printf("Storage Server Start Error: %v\n", err)
		return nil
	}

	return &StorageServer{
		basePath:   base,
		videoids:   make([]string, 0),
		filenames:  make([]string, 0),
		grpcServer: server,
		db:         db,
	}
}

func (ss *StorageServer) WriteFile(ctx context.Context, req *proto.WriteRequest) (*proto.WriteResponse, error) {

	dirPath := filepath.Join(ss.basePath, req.VideoId)

	// Create the directory if it does not exist
	if err := os.MkdirAll(dirPath, os.ModePerm); err != nil {
		fmt.Printf("Create file failed: %v\n", err)
		return &proto.WriteResponse{}, err
	}

	filePath := filepath.Join(dirPath, req.Filename)

	err := os.WriteFile(filePath, req.Data, 0644)
	if err != nil {
		fmt.Printf("Write file failed: %v\n", err)
		return &proto.WriteResponse{}, err
	}

	ss.videoids = append(ss.videoids, req.VideoId)
	ss.filenames = append(ss.filenames, req.Filename)

	return &proto.WriteResponse{}, nil
}

func (ss *StorageServer) ReadFile(ctx context.Context, req *proto.ReadRequest) (*proto.ReadResponse, error) {
	filePath := filepath.Join(ss.basePath, req.VideoId, req.Filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Printf("Read file failed: %v\n", err)
		return &proto.ReadResponse{Data: nil}, err
	}
	return &proto.ReadResponse{Data: data}, nil
}

func (ss *StorageServer) ListFile(ctx context.Context, req *proto.ListRequest) (*proto.ListResponse, error) {
	videoIds := []string{}
	filenames := []string{}

	entries, err := os.ReadDir(ss.basePath)
	if err != nil {
		log.Printf("Failed to read storage directory: %v", err)
		return &proto.ListResponse{}, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			videoId := entry.Name()
			videoDir := filepath.Join(ss.basePath, videoId)

			files, err := os.ReadDir(videoDir)
			if err != nil {
				log.Printf("Failed to read videoId dir: %s, err: %v", videoId, err)
				continue
			}

			for _, file := range files {
				if !file.IsDir() {
					videoIds = append(videoIds, videoId)
					filenames = append(filenames, file.Name())
				}
			}
		}
	}

	fmt.Printf("ListFile: Number of files: %v\n", len(videoIds))
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

	// Only remove if transfer is successful
	filePath := filepath.Join(ss.basePath, req.VideoId, req.Filename)
	err = os.Remove(filePath)
	if err != nil {
		fmt.Printf("Remove file failed: %v\n", err)
		return &proto.SendResponse{}, err
	}

	// Remove entry from internal lists
	for i := 0; i < len(ss.videoids); i++ {
		if ss.videoids[i] == req.VideoId && ss.filenames[i] == req.Filename {
			ss.videoids = append(ss.videoids[:i], ss.videoids[i+1:]...)
			ss.filenames = append(ss.filenames[:i], ss.filenames[i+1:]...)
			break
		}
	}

	return &proto.SendResponse{}, nil
}

func (ss *StorageServer) Shutdown(ctx context.Context, req *proto.ShutdownRequest) (*proto.ShutdownResponse, error) {
	fmt.Println("Received shutdown request. Stopping server...")
	go func() {
		time.Sleep(500 * time.Millisecond)
		ss.grpcServer.GracefulStop()
	}()
	return &proto.ShutdownResponse{}, nil
}
