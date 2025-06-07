// Lab 7: Implement a SQLite video metadata service

package web

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type SQLiteVideoMetadataService struct {
	db *sql.DB
}

// Uncomment the following line to ensure SQLiteVideoMetadataService implements VideoMetadataService
var _ VideoMetadataService = (*SQLiteVideoMetadataService)(nil)

func NewSQLiteVideoMetadataService(dbPath string) (*SQLiteVideoMetadataService, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create the videos table if it doesn't exist
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS videos (
			id TEXT PRIMARY KEY,
			uploaded_at TIMESTAMP
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	return &SQLiteVideoMetadataService{db: db}, nil
}

// Create saves a new video metadata entry
func (s *SQLiteVideoMetadataService) Create(videoId string, uploadedAt time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO videos (id, uploaded_at) VALUES (?, ?)
	`, videoId, uploadedAt)
	if err != nil {
		return fmt.Errorf("failed to insert metadata: %w", err)
	}
	return nil
}

// Read retrieves metadata associated with a given video ID
func (s *SQLiteVideoMetadataService) Read(videoId string) (*VideoMetadata, error) {
	row := s.db.QueryRow(`
		SELECT id, uploaded_at FROM videos WHERE id = ?
	`, videoId)

	var id string
	var uploadedAt time.Time

	err := row.Scan(&id, &uploadedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	return &VideoMetadata{
		Id:         id,
		UploadedAt: uploadedAt,
	}, nil
}

// List retrieves all video metadata entries
func (s *SQLiteVideoMetadataService) List() ([]VideoMetadata, error) {
	rows, err := s.db.Query(`
		SELECT id, uploaded_at FROM videos
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list metadata: %w", err)
	}
	defer rows.Close()

	var videos []VideoMetadata
	for rows.Next() {
		var id string
		var uploadedAt time.Time
		err := rows.Scan(&id, &uploadedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan metadata: %w", err)
		}
		videos = append(videos, VideoMetadata{Id: id, UploadedAt: uploadedAt})
	}

	return videos, nil
}
