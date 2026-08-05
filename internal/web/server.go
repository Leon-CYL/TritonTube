package web

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	uploadWorkerLimit = 64
	uploadBatchSize   = 4
)

type server struct {
	Addr string
	Port int

	metadataService VideoMetadataService
	contentService  VideoContentService

	mux        *http.ServeMux
	httpServer *http.Server
}

type VideoData struct {
	Id         string
	EscapedId  string
	UploadTime string
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

func NewServer(
	metadataService VideoMetadataService,
	contentService VideoContentService,
) *server {
	mux := http.NewServeMux()
	s := &server{
		metadataService: metadataService,
		contentService:  contentService,
		mux:             mux,
	}
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/videos/", s.handleVideo)
	mux.HandleFunc("/content/", s.handleVideoContent)
	mux.HandleFunc("/", s.handleIndex)
	s.httpServer = &http.Server{
		Handler: mux,
	}
	return s
}

func (s *server) Start(lis net.Listener) error {
	return s.httpServer.Serve(lis)
}

func (s *server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	videos, err := s.metadataService.List()
	if err != nil {
		http.Error(w, "Failed to retrieve video list", http.StatusInternalServerError)
		return
	}

	var videoList []VideoData
	for _, video := range videos {
		escapedId := url.PathEscape(video.Id)
		videoList = append(videoList, VideoData{
			Id:         video.Id,
			EscapedId:  escapedId,
			UploadTime: video.UploadedAt.Format("2006-01-02 15:04:05"),
		})
	}

	tmpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		http.Error(w, "Error parsing template", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	err = tmpl.Execute(w, videoList)
	if err != nil {
		log.Println("Error rendering template:", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	uploadStart := time.Now()
	videoId := "unknown"
	defer func() {
		log.Printf("Upload total time: video=%s duration=%.3f ms", videoId, durationMilliseconds(time.Since(uploadStart)))
	}()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error retrieving file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	videoId = strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))

	start := time.Now()
	existingVideo, err := s.metadataService.Read(videoId)
	if err != nil {
		http.Error(w, "Error checking video ID availability", http.StatusInternalServerError)
		return
	}
	if existingVideo != nil {
		http.Error(w, "Video ID already exists: "+videoId, http.StatusConflict)
		return
	}
	totalCheckTime := time.Since(start)
	log.Printf("Metadata duplicate check time: %.3f ms", durationMilliseconds(totalCheckTime))

	uploadDir := filepath.Join(os.TempDir(), "videos")

	if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
		http.Error(w, "Unable to create upload directory", http.StatusInternalServerError)
		return
	}

	videoPath := filepath.Join(uploadDir, header.Filename)
	dest, err := os.Create(videoPath)
	if err != nil {
		http.Error(w, "Unable to save file", http.StatusInternalServerError)
		return
	}
	defer dest.Close()

	start = time.Now()
	_, err = io.Copy(dest, file)
	if err != nil {
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}
	totalCopyTime := time.Since(start)
	log.Printf("MP4 file copy time: %.3f ms", durationMilliseconds(totalCopyTime))

	dashDir := filepath.Join(uploadDir, videoId)

	if err := os.MkdirAll(dashDir, os.ModePerm); err != nil {
		http.Error(w, "Unable to create DASH directory", http.StatusInternalServerError)
		return
	}

	manifestPath := filepath.Join(dashDir, "manifest.mpd")

	start = time.Now()
	cmd := exec.Command("ffmpeg",
		"-i", videoPath, // input file
		"-c:v", "libx264", // video codec
		"-preset", "veryfast", // faster software encoding
		"-threads", "2", // limit CPU usage on local machines
		"-c:a", "aac", // audio codec
		"-bf", "1", // max 1 B-frame
		"-keyint_min", "120", // minimum keyframe interval
		"-g", "120", // keyframe every 120 frames
		"-sc_threshold", "0", // scene change threshold
		"-b:v", "3000k", // video bitrate
		"-b:a", "128k", // audio bitrate
		"-f", "dash", // DASH format
		"-use_timeline", "1", // use timeline
		"-use_template", "1", // use template
		"-init_seg_name", "init-$RepresentationID$.m4s", // init segment naming
		"-media_seg_name", "chunk-$RepresentationID$-$Number%05d$.m4s", // media segment naming
		"-seg_duration", "4", // segment duration in seconds
		manifestPath, // output manifest file path
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		http.Error(w, "Error generating DASH content: "+err.Error()+"\n"+string(output), http.StatusInternalServerError)
		return
	}
	totalFFmpegTime := time.Since(start)
	log.Printf("FFmpeg transcoding time: %.3f ms", durationMilliseconds(totalFFmpegTime))

	start = time.Now()
	entries, err := os.ReadDir(dashDir)
	if err != nil {
		http.Error(w, "Error reading DASH directory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	totalScanTime := time.Since(start)
	log.Printf("DASH files scan time: %.3f ms", durationMilliseconds(totalScanTime))

	start = time.Now()
	fileCount, expectedCount, uploadErr := s.storeDASHFiles(videoId, dashDir, entries)

	if uploadErr != nil {
		log.Printf("DASH upload failed after storing %d/%d files: %v", fileCount, expectedCount, uploadErr)
		http.Error(w, "Failed to store DASH files", http.StatusInternalServerError)
		return
	}
	if expectedCount == 0 {
		http.Error(w, "No DASH files were generated", http.StatusInternalServerError)
		return
	}
	if fileCount != expectedCount {
		log.Printf("Only %d/%d files were written to storage", fileCount, expectedCount)
		http.Error(w, "Failed to store all DASH files", http.StatusInternalServerError)
		return
	}

	totalWriteTime := time.Since(start)
	log.Printf(
		"DASH storage time: video=%s files=%d duration=%.3f ms",
		videoId,
		fileCount,
		durationMilliseconds(totalWriteTime),
	)

	start = time.Now()
	err = s.metadataService.Create(videoId, time.Now())
	if err != nil {
		http.Error(w, "Error saving metadata: "+err.Error(), http.StatusInternalServerError)
		return
	}
	totalMetadataTime := time.Since(start)
	log.Printf("Metadata create time: %.3f ms", durationMilliseconds(totalMetadataTime))

	log.Printf("File successfully uploaded: %s\n", header.Filename)
	log.Printf("DASH content generated at: %s\n", manifestPath)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// storeDASHFiles uses a fixed-size worker pool. Each job reads a small group of
// DASH files and sends it through one or more batch gRPC writes. The number of
// goroutines and the in-memory data per job remain bounded for long videos.
func (s *server) storeDASHFiles(videoID, dashDir string, entries []os.DirEntry) (int, int, error) {
	regularEntries := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			regularEntries = append(regularEntries, entry)
		}
	}

	expectedCount := len(regularEntries)
	if expectedCount == 0 {
		return 0, 0, nil
	}

	jobs := make(chan []os.DirEntry)
	workerCount := (expectedCount + uploadBatchSize - 1) / uploadBatchSize
	if workerCount > uploadWorkerLimit {
		workerCount = uploadWorkerLimit
	}

	var wg sync.WaitGroup
	var resultMu sync.Mutex
	writtenCount := 0
	var firstErr error

	worker := func() {
		defer wg.Done()
		for batch := range jobs {
			files := make([]ContentFile, 0, len(batch))
			for _, entry := range batch {
				filename := entry.Name()
				data, err := os.ReadFile(filepath.Join(dashDir, filename))
				if err != nil {
					resultMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("read DASH file %s: %w", filename, err)
					}
					resultMu.Unlock()
					files = nil
					break
				}
				files = append(files, ContentFile{VideoID: videoID, Filename: filename, Data: data})
			}
			if len(files) == 0 {
				continue
			}

			count, err := s.contentService.WriteBatch(files)
			resultMu.Lock()
			writtenCount += count
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("store DASH batch: %w", err)
			} else if err == nil && count != len(files) && firstErr == nil {
				firstErr = fmt.Errorf("store DASH batch: wrote %d of %d files", count, len(files))
			}
			resultMu.Unlock()
		}
	}

	wg.Add(workerCount)
	for range workerCount {
		go worker()
	}
	for start := 0; start < len(regularEntries); start += uploadBatchSize {
		end := start + uploadBatchSize
		if end > len(regularEntries) {
			end = len(regularEntries)
		}
		jobs <- regularEntries[start:end]
	}
	close(jobs)
	wg.Wait()

	return writtenCount, expectedCount, firstErr
}

func (s *server) handleVideo(w http.ResponseWriter, r *http.Request) {
	videoId := r.URL.Path[len("/videos/"):]
	log.Println("Video ID:", videoId)

	metadata, err := s.metadataService.Read(videoId)
	if err != nil || metadata == nil {
		http.Error(w, "Video not found: "+videoId, http.StatusNotFound)
		return
	}

	data := struct {
		Id         string
		UploadedAt string
	}{
		Id:         metadata.Id,
		UploadedAt: metadata.UploadedAt.Format("2006-01-02 15:04:05"),
	}

	tmpl, err := template.New("video").Parse(videoHTML)
	if err != nil {
		http.Error(w, "Error parsing video template", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	if err := tmpl.Execute(w, data); err != nil {
		log.Println("Error rendering video template:", err)
		http.Error(w, "Failed to render video page", http.StatusInternalServerError)
	}
}

func (s *server) handleVideoContent(w http.ResponseWriter, r *http.Request) {
	requestStart := time.Now()
	videoId := "unknown"
	filename := "unknown"
	defer func() {
		log.Printf(
			"Content request total time: video=%s file=%s duration=%.3f ms",
			videoId,
			filename,
			durationMilliseconds(time.Since(requestStart)),
		)
	}()

	videoId = r.URL.Path[len("/content/"):]
	parts := strings.Split(videoId, "/")
	if len(parts) != 2 {
		http.Error(w, "Invalid content path", http.StatusBadRequest)
		return
	}
	videoId = parts[0]
	filename = parts[1]
	log.Println("Video ID:", videoId, "Filename:", filename)

	content, err := s.contentService.Read(videoId, filename)

	if err != nil || content == nil || len(content) == 0 {
		log.Println("Video content not Found: " + filename)
		http.Error(w, "Video content not Found", http.StatusInternalServerError)
		return
	}

	var contentType string
	switch {
	case strings.HasSuffix(filename, ".mpd"):
		contentType = "application/dash+xml"
	case strings.HasSuffix(filename, ".m4s"), strings.HasSuffix(filename, ".mp4"):
		contentType = "video/mp4"
	default:
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range")
	w.Header().Set("Accept-Ranges", "bytes")

	start := time.Now()
	if _, err := w.Write(content); err != nil {
		log.Printf("Error writing response: %v", err)
	}
	httpTime := time.Since(start)
	log.Printf("HTTP write time: %.3f ms", durationMilliseconds(httpTime))
}
