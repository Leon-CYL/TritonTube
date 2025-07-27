// Lab 7: Implement a web server

package web

import (
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

type server struct {
	Addr string
	Port int

	metadataService VideoMetadataService
	contentService  VideoContentService

	mux *http.ServeMux
}

type VideoData struct {
	Id         string
	EscapedId  string
	UploadTime string
}

func NewServer(
	metadataService VideoMetadataService,
	contentService VideoContentService,
) *server {
	return &server{
		metadataService: metadataService,
		contentService:  contentService,
	}
}

func (s *server) Start(lis net.Listener) error {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/upload", s.handleUpload)
	s.mux.HandleFunc("/videos/", s.handleVideo)
	s.mux.HandleFunc("/content/", s.handleVideoContent)
	s.mux.HandleFunc("/", s.handleIndex)

	return http.Serve(lis, s.mux)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Fetch all video metadata from the service
	videos, err := s.metadataService.List()
	if err != nil {
		http.Error(w, "Failed to retrieve video list", http.StatusInternalServerError)
		return
	}

	// Prepare the video data for the template
	var videoList []VideoData
	for _, video := range videos {
		escapedId := url.PathEscape(video.Id)
		videoList = append(videoList, VideoData{
			Id:         video.Id,
			EscapedId:  escapedId,
			UploadTime: video.UploadedAt.Format("2006-01-02 15:04:05"),
		})
	}

	// Parse and render the template
	tmpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		http.Error(w, "Error parsing template", http.StatusInternalServerError)
		return
	}

	// Set response headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Execute the template with the video list
	err = tmpl.Execute(w, videoList)
	if err != nil {
		log.Println("Error rendering template:", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Retrieve the file from form data
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error retrieving file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	videoId := strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))

	existingVideo, err := s.metadataService.Read(videoId)
	if err != nil {
		http.Error(w, "Error checking video ID availability", http.StatusInternalServerError)
		return
	}
	if existingVideo != nil {
		http.Error(w, "Video ID already exists: "+videoId, http.StatusConflict)
		return
	}

	// Save the uploaded file to disk
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

	_, err = io.Copy(dest, file)
	if err != nil {
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}

	// Create a directory for DASH output using the video ID
	dashDir := filepath.Join(uploadDir, videoId)

	if err := os.MkdirAll(dashDir, os.ModePerm); err != nil {
		http.Error(w, "Unable to create DASH directory", http.StatusInternalServerError)
		return
	}

	manifestPath := filepath.Join(dashDir, "manifest.mpd")

	// Run FFmpeg to generate MPEG-DASH segments and manifest
	cmd := exec.Command("ffmpeg",
		"-i", videoPath, // input file
		"-c:v", "libx264", // video codec
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

	// Run the FFmpeg command and capture any errors
	if output, err := cmd.CombinedOutput(); err != nil {
		http.Error(w, "Error generating DASH content: "+err.Error()+"\n"+string(output), http.StatusInternalServerError)
		return
	}

	// Store the converted DASH files using VideoContentService
	entries, err := os.ReadDir(dashDir)
	if err != nil {
		http.Error(w, "Error reading DASH directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	fileCount := 0
	totalWriteTime := time.Duration(0)
	var wg sync.WaitGroup
	maxWorkers := 64
	sem := make(chan struct{}, maxWorkers)
	start := time.Now()

	for _, entry := range entries {
		if !entry.IsDir() {
			wg.Add(1)
			sem <- struct{}{}
			go func(entry os.DirEntry) {
				defer wg.Done()
				defer func() { <-sem }()
				if !entry.IsDir() {
					fileName := entry.Name()
					filePath := filepath.Join(dashDir, fileName)

					data, err := os.ReadFile(filePath)
					if err != nil {
						log.Println(w, "Error reading DASH file: "+fileName, http.StatusInternalServerError)
						return
					}

					if err := s.contentService.Write(videoId, fileName, data); err != nil {
						log.Println(w, "Error storing DASH file: "+fileName, http.StatusInternalServerError)
						return
					}

					fileCount++
				}
			}(entry)
		}
	}
	wg.Wait()

	// Storage Node write performance metrics
	totalWriteTime = time.Since(start)
	log.Printf("Uploaded %d DASH files for %s in %s", fileCount, videoId, totalWriteTime)
	if fileCount > 0 {
		avg := totalWriteTime / time.Duration(fileCount)
		log.Printf("Average write time per file: %s", avg)
	}


	err = s.metadataService.Create(videoId, time.Now())
	if err != nil {
		http.Error(w, "Error saving metadata: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Log success messages before redirection
	log.Printf("File successfully uploaded: %s\n", header.Filename)
	log.Printf("DASH content generated at: %s\n", manifestPath)

	// Redirect to the landing page after successful upload
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) handleVideo(w http.ResponseWriter, r *http.Request) {
	// Extract the video ID from the URL
	videoId := r.URL.Path[len("/videos/"):]
	log.Println("Video ID:", videoId)

	// Retrieve video metadata
	metadata, err := s.metadataService.Read(videoId)
	if err != nil || metadata == nil {
		http.Error(w, "Video not found: "+videoId, http.StatusNotFound)
		return
	}

	// Prepare the data for the template
	data := struct {
		Id         string
		UploadedAt string
	}{
		Id:         metadata.Id,
		UploadedAt: metadata.UploadedAt.Format("2006-01-02 15:04:05"),
	}

	// Parse and render the video template
	tmpl, err := template.New("video").Parse(videoHTML)
	if err != nil {
		http.Error(w, "Error parsing video template", http.StatusInternalServerError)
		return
	}

	// Set response headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Execute the template with the video metadata
	if err := tmpl.Execute(w, data); err != nil {
		log.Println("Error rendering video template:", err)
		http.Error(w, "Failed to render video page", http.StatusInternalServerError)
	}
}

func (s *server) handleVideoContent(w http.ResponseWriter, r *http.Request) {
	videoId := r.URL.Path[len("/content/"):]
	parts := strings.Split(videoId, "/")
	if len(parts) != 2 {
		http.Error(w, "Invalid content path", http.StatusBadRequest)
		return
	}
	videoId = parts[0]
	filename := parts[1]
	log.Println("Video ID:", videoId, "Filename:", filename)

	start := time.Now()
	content, err := s.contentService.Read(videoId, filename)
	log.Printf("Read %s/%s (%d bytes) in %s\n", videoId, filename, len(content), time.Since(start))

	if err != nil || content == nil || len(content) == 0 {
		log.Println("Video content not Found: " + filename)
		http.Error(w, "Video content not Found", http.StatusInternalServerError)
		return
	}

	// Set headers
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

	// Stream content
	if _, err := w.Write(content); err != nil {
		log.Printf("Error writing response: %v", err)
	}
}
