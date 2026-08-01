// storage.go: Filesystem storage for video content files.

package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"tritontube/internal/proto"
)

// Implement a network video content service (server)
type StorageServer struct {
	proto.UnimplementedVideoContentStorageServiceServer
	basePath string
}

func NewStorageServer(base string) *StorageServer {
	if err := os.MkdirAll(base, os.ModePerm); err != nil {
		fmt.Printf("Failed to create storage directory: %v\n", err)
		return nil
	}

	return &StorageServer{
		basePath: base,
	}
}

// WriteFile writes a single file to the server's storage directory.
func (ss *StorageServer) WriteFile(ctx context.Context, req *proto.WriteRequest) (*proto.WriteResponse, error) {
	filePath, err := ss.filePath(req.VideoId, req.Filename)
	if err != nil {
		return &proto.WriteResponse{}, err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		log.Printf("Storage: Create directory failed: %v\n", err)
		return &proto.WriteResponse{}, err
	}

	if err := os.WriteFile(filePath, req.Data, 0644); err != nil {
		log.Printf("Storage: Write file failed: %v\n", err)
		return &proto.WriteResponse{}, err
	}

	return &proto.WriteResponse{}, nil
}

// WriteFiles writes a batch of files to the server's storage directory.
func (ss *StorageServer) WriteFiles(ctx context.Context, req *proto.BatchWriteRequest) (*proto.BatchWriteResponse, error) {
	var count uint32
	for _, entry := range req.Entries {
		if err := ctx.Err(); err != nil {
			return &proto.BatchWriteResponse{Cnt: count}, err
		}

		filePath, err := ss.filePath(entry.VideoId, entry.Filename)
		if err != nil {
			return &proto.BatchWriteResponse{Cnt: count}, err
		}

		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			log.Printf("Storage: Create directory failed: %v\n", err)
			return &proto.BatchWriteResponse{Cnt: count}, err
		}

		if err := os.WriteFile(filePath, entry.Data, 0644); err != nil {
			log.Printf("Storage: Write file failed: %v\n", err)
			return &proto.BatchWriteResponse{Cnt: count}, err
		}
		count++
	}

	return &proto.BatchWriteResponse{Cnt: count}, nil
}

// ReadFile reads a single file from the server's storage directory.
func (ss *StorageServer) ReadFile(ctx context.Context, req *proto.ReadRequest) (*proto.ReadResponse, error) {
	filePath, err := ss.filePath(req.VideoId, req.Filename)
	if err != nil {
		return &proto.ReadResponse{Data: nil}, err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("Storage: Read file failed: %v\n", err)
		return &proto.ReadResponse{Data: nil}, err
	}

	return &proto.ReadResponse{Data: data}, nil
}

// ReadFiles reads every regular file under the server's storage directory.
func (ss *StorageServer) ReadFiles(ctx context.Context, req *proto.BatchReadRequest) (*proto.BatchReadResponse, error) {
	entries := make([]*proto.FileEntry, 0)

	err := filepath.WalkDir(ss.basePath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		relativePath, err := filepath.Rel(ss.basePath, path)
		if err != nil {
			return err
		}
		parts := splitStoredPath(relativePath)
		if len(parts) != 2 {
			log.Printf("Storage: Skipping file outside a video directory: %s\n", path)
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}

		entries = append(entries, &proto.FileEntry{
			VideoId:  parts[0],
			Filename: parts[1],
			Data:     data,
		})
		return nil
	})

	if err != nil {
		log.Printf("Storage: Read files failed: %v\n", err)
		return &proto.BatchReadResponse{Entries: entries}, err
	}

	log.Printf("Storage: Found %d files\n", len(entries))
	return &proto.BatchReadResponse{Entries: entries}, nil
}

func (ss *StorageServer) filePath(videoID, filename string) (string, error) {
	if videoID == "" || filename == "" {
		return "", fmt.Errorf("video ID and filename must not be empty")
	}

	basePath, err := filepath.Abs(ss.basePath)
	if err != nil {
		return "", err
	}
	filePath := filepath.Join(basePath, videoID, filename)
	relativePath, err := filepath.Rel(basePath, filePath)
	if err != nil {
		return "", err
	}
	if relativePath == ".." || len(relativePath) >= 3 && relativePath[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("file path escapes storage directory")
	}

	return filePath, nil
}

func splitStoredPath(path string) []string {
	for i, char := range path {
		if char == filepath.Separator {
			return []string{path[:i], path[i+1:]}
		}
	}
	return nil
}

func isDirectoryNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
