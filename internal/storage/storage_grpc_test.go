package storage

import (
	"bytes"
	"context"
	"net"
	"testing"

	"tritontube/internal/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 4 * 1024 * 1024

func newGRPCStorageClient(
	t *testing.T,
) proto.VideoContentStorageServiceClient {
	t.Helper()

	listener := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()

	storageServer := NewStorageServer(t.TempDir())
	if storageServer == nil {
		t.Fatal("NewStorageServer returned nil")
	}

	proto.RegisterVideoContentStorageServiceServer(
		grpcServer,
		storageServer,
	)

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			t.Errorf("gRPC server failed: %v", err)
		}
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(
			func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			},
		),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		grpcServer.Stop()
		listener.Close()
		t.Fatalf("Create gRPC client connection: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()
		grpcServer.Stop()
		listener.Close()
	})

	return proto.NewVideoContentStorageServiceClient(conn)
}

func TestStorageGRPCWriteFileReadFile(t *testing.T) {
	client := newGRPCStorageClient(t)

	writeRequest := &proto.WriteRequest{
		VideoId:  "video-123",
		Filename: "manifest.mpd",
		Data:     []byte("manifest content"),
	}

	_, err := client.WriteFile(t.Context(), writeRequest)
	if err != nil {
		t.Fatalf("WriteFile RPC failed: %v", err)
	}

	readResponse, err := client.ReadFile(
		t.Context(),
		&proto.ReadRequest{
			VideoId:  writeRequest.VideoId,
			Filename: writeRequest.Filename,
		},
	)
	if err != nil {
		t.Fatalf("ReadFile RPC failed: %v", err)
	}

	if !bytes.Equal(readResponse.Data, writeRequest.Data) {
		t.Fatalf(
			"ReadFile data = %q, want %q",
			readResponse.Data,
			writeRequest.Data,
		)
	}
}

func TestStorageGRPCWriteFilesReadFiles(t *testing.T) {
	client := newGRPCStorageClient(t)

	entries := []*proto.FileEntry{
		{
			VideoId:  "video-123",
			Filename: "manifest.mpd",
			Data:     []byte("manifest"),
		},
		{
			VideoId:  "video-123",
			Filename: "init-0.m4s",
			Data:     []byte("initialization segment"),
		},
		{
			VideoId:  "video-123",
			Filename: "chunk-0-00001.m4s",
			Data:     []byte("media segment"),
		},
	}

	writeResponse, err := client.WriteFiles(
		t.Context(),
		&proto.BatchWriteRequest{Entries: entries},
	)
	if err != nil {
		t.Fatalf("WriteFiles RPC failed: %v", err)
	}

	if writeResponse.Cnt != uint32(len(entries)) {
		t.Fatalf(
			"WriteFiles count = %d, want %d",
			writeResponse.Cnt,
			len(entries),
		)
	}

	readResponse, err := client.ReadFiles(
		t.Context(),
		&proto.BatchReadRequest{},
	)
	if err != nil {
		t.Fatalf("ReadFiles RPC failed: %v", err)
	}

	if len(readResponse.Entries) != len(entries) {
		t.Fatalf(
			"ReadFiles returned %d entries, want %d",
			len(readResponse.Entries),
			len(entries),
		)
	}

	expected := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		key := entry.VideoId + "/" + entry.Filename
		expected[key] = entry.Data
	}

	for _, entry := range readResponse.Entries {
		key := entry.VideoId + "/" + entry.Filename

		expectedData, ok := expected[key]
		if !ok {
			t.Fatalf("ReadFiles returned unexpected entry %q", key)
		}

		if !bytes.Equal(entry.Data, expectedData) {
			t.Fatalf(
				"Data for %q = %q, want %q",
				key,
				entry.Data,
				expectedData,
			)
		}

		delete(expected, key)
	}

	if len(expected) != 0 {
		t.Fatalf("ReadFiles omitted %d entries", len(expected))
	}
}

func TestStorageGRPCReadMissingFile(t *testing.T) {
	client := newGRPCStorageClient(t)

	_, err := client.ReadFile(
		t.Context(),
		&proto.ReadRequest{
			VideoId:  "missing-video",
			Filename: "missing-file.m4s",
		},
	)
	if err == nil {
		t.Fatal("ReadFile expected an error for a missing file")
	}
}
