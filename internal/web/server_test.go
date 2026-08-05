package web

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type recordingContentService struct {
	mu         sync.Mutex
	files      map[string][]byte
	batchSizes []int
}

func (service *recordingContentService) Read(string, string) ([]byte, error) {
	return nil, nil
}

func (service *recordingContentService) Write(videoID, filename string, data []byte) error {
	_, err := service.WriteBatch([]ContentFile{{VideoID: videoID, Filename: filename, Data: data}})
	return err
}

func (service *recordingContentService) WriteBatch(files []ContentFile) (int, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.batchSizes = append(service.batchSizes, len(files))
	for _, file := range files {
		service.files[file.VideoID+"/"+file.Filename] = append([]byte(nil), file.Data...)
	}
	return len(files), nil
}

func TestStoreDASHFilesUsesBoundedBatches(t *testing.T) {
	dashDir := t.TempDir()
	const fileCount = uploadWorkerLimit*uploadBatchSize + 7
	for index := range fileCount {
		filename := fmt.Sprintf("chunk-%05d.m4s", index)
		if err := os.WriteFile(filepath.Join(dashDir, filename), []byte(filename), 0644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	entries, err := os.ReadDir(dashDir)
	if err != nil {
		t.Fatalf("read fixture directory: %v", err)
	}

	content := &recordingContentService{files: make(map[string][]byte)}
	server := &server{contentService: content}
	written, expected, err := server.storeDASHFiles("video", dashDir, entries)
	if err != nil {
		t.Fatalf("storeDASHFiles failed: %v", err)
	}
	if written != fileCount || expected != fileCount {
		t.Fatalf("counts = written %d expected %d, want %d", written, expected, fileCount)
	}
	if len(content.files) != fileCount {
		t.Fatalf("stored files = %d, want %d", len(content.files), fileCount)
	}
	for _, size := range content.batchSizes {
		if size < 1 || size > uploadBatchSize {
			t.Fatalf("batch size = %d, want 1..%d", size, uploadBatchSize)
		}
	}
}
