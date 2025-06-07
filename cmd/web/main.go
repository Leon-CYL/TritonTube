package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
	"tritontube/internal/proto"
	"tritontube/internal/web"

	"google.golang.org/grpc"
)

// printUsage prints the usage information for the application
func printUsage() {
	fmt.Println("Usage: ./program [OPTIONS] METADATA_TYPE METADATA_OPTIONS CONTENT_TYPE CONTENT_OPTIONS")
	fmt.Println()
	fmt.Println("Arguments:")
	fmt.Println("  METADATA_TYPE         Metadata service type (sqlite, etcd)")
	fmt.Println("  METADATA_OPTIONS      Options for metadata service (e.g., db path)")
	fmt.Println("  CONTENT_TYPE          Content service type (fs, nw)")
	fmt.Println("  CONTENT_OPTIONS       Options for content service (e.g., base dir, network addresses)")
	fmt.Println()
	fmt.Println("Options:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Example: ./program sqlite db.db fs /path/to/videos")
}

func main() {
	// Define flags
	port := flag.Int("port", 8080, "Port number for the web server")
	host := flag.String("host", "localhost", "Host address for the web server")

	// Set custom usage message
	flag.Usage = printUsage

	// Parse flags
	flag.Parse()

	// Check if the correct number of positional arguments is provided
	if len(flag.Args()) != 4 {
		fmt.Println("Error: Incorrect number of arguments")
		printUsage()
		return
	}

	// Parse positional arguments
	metadataServiceType := flag.Arg(0)
	metadataServiceOptions := flag.Arg(1)
	contentServiceType := flag.Arg(2)
	contentServiceOptions := flag.Arg(3)

	// Validate port number (already an int from flag, check if positive)
	if *port <= 0 {
		fmt.Println("Error: Invalid port number:", *port)
		printUsage()
		return
	}

	var err error

	// Construct metadata service
	var metadataService web.VideoMetadataService
	fmt.Println("Creating metadata service of type", metadataServiceType, "with options", metadataServiceOptions)
	// TODO: Implement metadata service creation logic
	switch metadataServiceType {
	case "sqlite":
		metadataService, err = web.NewSQLiteVideoMetadataService(metadataServiceOptions)
		if err != nil {
			fmt.Printf("Create sqlite db error: %v.\n", err)
			return
		}
	case "etcd":
		nodes := strings.Split(metadataServiceOptions, ",")
		metadataService, err = web.NewEtcdVideoMetadataService(nodes)

		if err != nil {
			fmt.Printf("MetadataService create failed: %v\n", err)
			return
		}

	default:
		fmt.Printf("Unknown File System type [sqlite/etcd]: %s\n", metadataServiceType)
		return
	}

	// Construct content service
	var contentService web.VideoContentService
	fmt.Println("Creating content service of type", contentServiceType, "with options", contentServiceOptions)
	// TODO: Implement content service creation logic
	switch contentServiceType {
	case "fs":
		contentService, err = web.NewFSVideoContentService(contentServiceOptions)
		if err != nil {
			fmt.Printf("Create content service error: %v.\n", err)
			return
		}

	case "nw":
		nodes := strings.Split(contentServiceOptions, ",")

		if len(nodes) < 2 {
			fmt.Println("Invalid contentServiceOptions: expected at least one admin address and one node")
			return
		}

		contentService = web.NewNetworkVideoContentService(nodes[1:])

		grpcServer := grpc.NewServer()
		proto.RegisterVideoContentAdminServiceServer(grpcServer, contentService.(*web.NetworkVideoContentService))

		lis, err := net.Listen("tcp", nodes[0])
		if err != nil {
			log.Fatalf("Failed to listen: %v", err)
			return
		}
		fmt.Printf("Admin server %s is running...\n", nodes[0])

		go func() {
			if err := grpcServer.Serve(lis); err != nil {
				log.Fatalf("Failed to serve: %v", err)
				return
			}
		}()

	default:
		fmt.Printf("Unknown File System type [fs/nw]: %s\n", contentServiceType)
		return
	}

	// Start the server
	server := web.NewServer(metadataService, contentService)
	listenAddr := fmt.Sprintf("%s:%d", *host, *port)
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Println("Error starting listener:", err)
		return
	}
	defer lis.Close()

	fmt.Println("Starting web server on", listenAddr)
	err = server.Start(lis)
	if err != nil {
		fmt.Println("Error starting server:", err)
		return
	}
}
