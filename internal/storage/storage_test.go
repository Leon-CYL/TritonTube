package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"tritontube/internal/proto"

	"google.golang.org/grpc"
)

func newServer(t *testing.T) *StorageServer {
	t.Helper()

	baseDir := t.TempDir()
	grpcServer := grpc.NewServer()
	t.Cleanup(grpcServer.Stop)

	return NewStorageServer(baseDir, grpcServer)
}

func TestWriteFile(t *testing.T) {
	server := newServer(t)
	writeReq := &proto.WriteRequest{
		VideoId:  "abc123",
		Filename: "test.txt",
		Data:     []byte("Hello World Test"),
	}

	_, err := server.WriteFile(t.Context(), writeReq)
	if err != nil {
		t.Fatalf("Write File Error: %v\n", err)
	}

	// Check base directory exist
	info, err := os.Stat(server.basePath)
	if err != nil {
		t.Fatalf("Stat Base Directory Error: %v\n", err)
	}

	// Check whether base directory is a directory
	if !info.IsDir() {
		t.Fatalf("The Base dir should be a directory: %v\n", info)
	}

	// Check whether the file is written to the file system
	filePath := filepath.Join(server.basePath, writeReq.VideoId, writeReq.Filename)
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("File is not written to the file system: %s: %v\n", filePath, err)
	}
	if !fileInfo.Mode().IsRegular() {
		t.Fatalf("Written path should be a regular file: %s\n", filePath)
	}
}

func TestWriteFileReadFile(t *testing.T) {
	server := newServer(t)

	writeReq := &proto.WriteRequest{
		VideoId:  "abc123",
		Filename: "test.txt",
		Data:     []byte("Hello World Test"),
	}

	readReq := &proto.ReadRequest{
		VideoId:  "abc123",
		Filename: "test.txt",
	}

	_, err := server.WriteFile(t.Context(), writeReq)
	if err != nil {
		t.Fatalf("Write File Error: %v\n", err)
	}

	readRepsonse, err := server.ReadFile(t.Context(), readReq)
	if err != nil {
		t.Fatalf("Read File Error: %v\n", err)
	}

	if !bytes.Equal(readRepsonse.Data, writeReq.Data) {
		t.Fatalf("The read data is not equal to the write data:\nread: %v\n, write:%v\n", readRepsonse.Data, writeReq.Data)
	}
}

func TestFileOverwrite(t *testing.T) {
	server := newServer(t)

	oldWriteReq := &proto.WriteRequest{
		VideoId:  "abc123",
		Filename: "test.txt",
		Data:     []byte("Hello World Test"),
	}

	newWriteReq := &proto.WriteRequest{
		VideoId:  "abc123",
		Filename: "test.txt",
		Data:     []byte("Hello World Test 1"),
	}

	readReq := &proto.ReadRequest{
		VideoId:  "abc123",
		Filename: "test.txt",
	}

	_, err := server.WriteFile(t.Context(), oldWriteReq)
	if err != nil {
		t.Fatalf("Write File Error: %v\n", err)
	}

	_, err = server.WriteFile(t.Context(), newWriteReq)
	if err != nil {
		t.Fatalf("Write File Error: %v\n", err)
	}

	readRepsonse, err := server.ReadFile(t.Context(), readReq)
	if err != nil {
		t.Fatalf("Read File Error: %v\n", err)
	}

	if !bytes.Equal(readRepsonse.Data, newWriteReq.Data) {
		t.Fatalf("The read data is not equal to the new write data:\nread: %v\n, write:%v\n", readRepsonse.Data, newWriteReq.Data)
	}
}

func TestWriteFiles(t *testing.T) {
	server := newServer(t)

	entries := make([]*proto.FileEntry, 0)
	count := 500

	for i := range count {
		entries = append(entries, &proto.FileEntry{
			VideoId:  fmt.Sprintf("abc%d", i),
			Filename: fmt.Sprintf("test%d.txt", i),
			Data:     fmt.Appendf(nil, "Hello World Test %d", i),
		})
	}

	batchWriteFilesReq := &proto.BatchWriteRequest{
		Entries: entries,
	}

	response, err := server.WriteFiles(t.Context(), batchWriteFilesReq)
	if err != nil {
		t.Fatalf("Write Files Error: %v\n", err)
	}

	if response.Cnt != uint32(count) {
		t.Fatalf("Only %d/%d files written to the filesystem\n", response.Cnt, count)
	}
}

func TestWriteFilesReadFiles(t *testing.T) {
	server := newServer(t)

	entries := make([]*proto.FileEntry, 0)
	files := make(map[string][]byte) // videoId/Filename : Data
	count := 500

	for i := range count {
		videoId := fmt.Sprintf("abc%d", i)
		filename := fmt.Sprintf("test%d.txt", i)
		data := fmt.Appendf(nil, "Hello World Test %d", i)

		entries = append(entries, &proto.FileEntry{
			VideoId:  videoId,
			Filename: filename,
			Data:     data,
		})

		files[filepath.Join(videoId, filename)] = data
	}

	batchWriteFilesReq := &proto.BatchWriteRequest{
		Entries: entries,
	}

	writeResponse, err := server.WriteFiles(t.Context(), batchWriteFilesReq)
	if err != nil {
		t.Fatalf("Write Files Error: %v\n", err)
	}

	if writeResponse.Cnt != uint32(count) {
		t.Fatalf("Only %d/%d files written to the filesystem\n", writeResponse.Cnt, count)
	}

	batchReadFilesreq := &proto.BatchReadRequest{}

	readResponse, err := server.ReadFiles(t.Context(), batchReadFilesreq)
	if err != nil {
		t.Fatalf("Read Files Error: %v\n", err)
	}

	if len(readResponse.Entries) != int(writeResponse.Cnt) {
		t.Fatalf("Only %d/%d of files read\n", len(readResponse.Entries), writeResponse.Cnt)
	}

	for _, entry := range readResponse.Entries {
		path := filepath.Join(entry.VideoId, entry.Filename)
		expected, ok := files[path]
		if !ok {
			t.Fatalf("ReadFiles returned unexpected file: %s\n", path)
		}
		if !bytes.Equal(expected, entry.Data) {
			t.Fatalf("File %s: Data mismatch\nWrite Data: %v\nRead Data: %v\n", path, expected, entry.Data)
		}
		delete(files, path)
	}

	if len(files) != 0 {
		t.Fatalf("ReadFiles did not return %d expected files\n", len(files))
	}
}

func TestEmptyStorageServer(t *testing.T) {
	server := newServer(t)

	batchReadFilesReq := &proto.BatchReadRequest{}

	response, err := server.ReadFiles(t.Context(), batchReadFilesReq)
	if err != nil {
		t.Fatalf("Read Files Error: %v\n", err)
	}

	if len(response.Entries) != 0 {
		t.Fatalf("Expect 0 File read but %d files read\n", len(response.Entries))
	}
}

func TestReadFileError(t *testing.T) {
	server := newServer(t)

	readFileReq := &proto.ReadRequest{
		VideoId:  "abc123",
		Filename: "test.txt",
	}

	_, err := server.ReadFile(t.Context(), readFileReq)
	if err == nil {
		t.Fatalf("Expect a read file Error for not exist file\n")
	}
}

func TestWriteFileRejectsInvalidPath(t *testing.T) {
	server := newServer(t)

	tests := []struct {
		name     string
		videoID  string
		filename string
	}{
		{
			name:     "empty video ID",
			videoID:  "",
			filename: "test.txt",
		},
		{
			name:     "empty filename",
			videoID:  "abc123",
			filename: "",
		},
		{
			name:     "video ID escapes base directory",
			videoID:  "../outside",
			filename: "test.txt",
		},
		{
			name:     "filename escapes base directory",
			videoID:  "abc123",
			filename: "../../outside.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.WriteFile(t.Context(), &proto.WriteRequest{
				VideoId:  tt.videoID,
				Filename: tt.filename,
				Data:     []byte("data"),
			})
			if err == nil {
				t.Fatalf("WriteFile(%q, %q) expected an error\n", tt.videoID, tt.filename)
			}
		})
	}
}

func TestWriteFilesEmptyBatch(t *testing.T) {
	server := newServer(t)

	response, err := server.WriteFiles(t.Context(), &proto.BatchWriteRequest{})
	if err != nil {
		t.Fatalf("WriteFiles Empty Batch Error: %v\n", err)
	}
	if response.Cnt != 0 {
		t.Fatalf("WriteFiles empty batch count = %d, want 0\n", response.Cnt)
	}
}

func TestWriteFilesCancelledContext(t *testing.T) {
	server := newServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	response, err := server.WriteFiles(ctx, &proto.BatchWriteRequest{
		Entries: []*proto.FileEntry{
			{
				VideoId:  "abc123",
				Filename: "test.txt",
				Data:     []byte("data"),
			},
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteFiles error = %v, want context.Canceled\n", err)
	}
	if response.Cnt != 0 {
		t.Fatalf("WriteFiles cancelled count = %d, want 0\n", response.Cnt)
	}

	filePath := filepath.Join(server.basePath, "abc123", "test.txt")
	if _, err := os.Stat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Cancelled WriteFiles created %s; stat error = %v\n", filePath, err)
	}
}

func TestReadFilesCancelledContext(t *testing.T) {
	server := newServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	response, err := server.ReadFiles(ctx, &proto.BatchReadRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadFiles error = %v, want context.Canceled\n", err)
	}
	if len(response.Entries) != 0 {
		t.Fatalf("ReadFiles cancelled response contains %d entries, want 0\n", len(response.Entries))
	}
}

func TestReadFilesSameVideoAndNestedFilename(t *testing.T) {
	server := newServer(t)
	entries := []*proto.FileEntry{
		{
			VideoId:  "abc123",
			Filename: "manifest.mpd",
			Data:     []byte("manifest"),
		},
		{
			VideoId:  "abc123",
			Filename: "chunk-00001.m4s",
			Data:     []byte("segment"),
		},
		{
			VideoId:  "abc123",
			Filename: filepath.Join("nested", "init.m4s"),
			Data:     []byte("init"),
		},
	}

	writeResponse, err := server.WriteFiles(t.Context(), &proto.BatchWriteRequest{Entries: entries})
	if err != nil {
		t.Fatalf("WriteFiles Error: %v\n", err)
	}
	if writeResponse.Cnt != uint32(len(entries)) {
		t.Fatalf("WriteFiles count = %d, want %d\n", writeResponse.Cnt, len(entries))
	}

	readResponse, err := server.ReadFiles(t.Context(), &proto.BatchReadRequest{})
	if err != nil {
		t.Fatalf("ReadFiles Error: %v\n", err)
	}
	if len(readResponse.Entries) != len(entries) {
		t.Fatalf("ReadFiles count = %d, want %d\n", len(readResponse.Entries), len(entries))
	}

	expected := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		expected[filepath.Join(entry.VideoId, entry.Filename)] = entry.Data
	}

	for _, entry := range readResponse.Entries {
		path := filepath.Join(entry.VideoId, entry.Filename)
		data, ok := expected[path]
		if !ok {
			t.Fatalf("ReadFiles returned unexpected file: %s\n", path)
		}
		if !bytes.Equal(data, entry.Data) {
			t.Fatalf("File %s: Data mismatch\nWrite Data: %v\nRead Data: %v\n", path, data, entry.Data)
		}
		delete(expected, path)
	}

	if len(expected) != 0 {
		t.Fatalf("ReadFiles did not return %d expected files\n", len(expected))
	}
}
