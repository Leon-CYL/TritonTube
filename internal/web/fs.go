// Lab 7: Implement a local filesystem video content service

package web

import (
	"os"
	"path/filepath"
)

// FSVideoContentService implements VideoContentService using the local filesystem.
type FSVideoContentService struct {
	basePath string
}

// Uncomment the following line to ensure FSVideoContentService implements VideoContentService
var _ VideoContentService = (*FSVideoContentService)(nil)

func NewFSVideoContentService(basePath string) (*FSVideoContentService, error) {
	// Ensure the base directory exists or create it
	if err := os.MkdirAll(basePath, os.ModePerm); err != nil {
		return nil, err
	}

	return &FSVideoContentService{basePath: basePath}, nil
}

func (cs *FSVideoContentService) Basepath() string {
	return cs.basePath
}

func (cs *FSVideoContentService) Read(videoId string, filename string) ([]byte, error) {
	filePath := filepath.Join(cs.basePath, videoId, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (cs *FSVideoContentService) Write(videoId string, filename string, data []byte) error {
	dirPath := filepath.Join(cs.basePath, videoId)

	// Create the directory if it does not exist
	if err := os.MkdirAll(dirPath, os.ModePerm); err != nil {
		return err
	}

	filePath := filepath.Join(dirPath, filename)

	err := os.WriteFile(filePath, data, 0644)
	if err != nil {
		return err
	}

	return nil
}
