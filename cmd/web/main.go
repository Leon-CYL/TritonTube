package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"tritontube/internal/proto"
	"tritontube/internal/web"

	"google.golang.org/grpc"
)

// printUsage prints the usage information for the application
func printUsage() {
	fmt.Println("Usage: ./program [OPTIONS] METADATA_TYPE METADATA_OPTIONS CONTENT_TYPE CONTENT_OPTIONS")
	fmt.Println()
	fmt.Println("Arguments:")
	fmt.Println("  METADATA_TYPE         Metadata service type (etcd)")
	fmt.Println("  METADATA_OPTIONS      Options for metadata service (e.g., db path)")
	fmt.Println("  CONTENT_TYPE          Content service type (fs, nw)")
	fmt.Println("  CONTENT_OPTIONS       Options for content service (e.g., base dir, network addresses)")
	fmt.Println()
	fmt.Println("Options:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Example: ./program etcd db.db fs /path/to/videos")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "web:", err)
		os.Exit(1)
	}
}

func run() error {
	port := flag.Int("port", 8080, "Port number for the web server")
	host := flag.String("host", "localhost", "Host address for the web server")

	flag.Usage = printUsage

	flag.Parse()

	if len(flag.Args()) != 4 {
		return errors.New("incorrect number of arguments; expected metadata and content configuration")
	}

	metadataServiceType := flag.Arg(0)
	metadataServiceOptions := flag.Arg(1)
	contentServiceType := flag.Arg(2)
	contentServiceOptions := flag.Arg(3)

	if *port <= 0 {
		return fmt.Errorf("invalid port number: %d", *port)
	}
	var err error

	var metadataService web.VideoMetadataService
	fmt.Println("Creating metadata service of type", metadataServiceType, "with options", metadataServiceOptions)
	switch metadataServiceType {
	case "etcd":
		nodes := strings.Split(metadataServiceOptions, ",")
		etcdService, createErr := web.NewEtcdVideoMetadataService(nodes)

		if createErr != nil {
			return fmt.Errorf("create metadata service: %w", createErr)
		}
		defer etcdService.Close()
		metadataService = etcdService

	default:
		return fmt.Errorf("unknown metadata service type %q; supported: etcd", metadataServiceType)
	}

	var contentService web.VideoContentService
	var grpcServer *grpc.Server
	fmt.Println("Creating content service of type", contentServiceType, "with options", contentServiceOptions)
	switch contentServiceType {
	case "nw":
		nodes := strings.Split(contentServiceOptions, ",")

		if len(nodes) < 2 {
			return errors.New("content options require one admin address and at least one storage node")
		}

		contentService = web.NewNetworkVideoContentService(nodes[1:])

		grpcServer = grpc.NewServer()
		proto.RegisterVideoContentAdminServiceServer(grpcServer, contentService.(*web.NetworkVideoContentService))

		lis, err := net.Listen("tcp", nodes[0])
		if err != nil {
			return fmt.Errorf("listen for admin gRPC on %s: %w", nodes[0], err)
		}
		defer lis.Close()
		fmt.Printf("Admin server %s is running...\n", nodes[0])

		go func() {
			if err := grpcServer.Serve(lis); err != nil {
				fmt.Fprintln(os.Stderr, "admin gRPC server:", err)
			}
		}()

	default:
		return fmt.Errorf("unknown content service type %q; supported: nw", contentServiceType)
	}

	server := web.NewServer(metadataService, contentService)
	listenAddr := fmt.Sprintf("%s:%d", *host, *port)
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen for HTTP on %s: %w", listenAddr, err)
	}
	defer lis.Close()

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	shutdownDone := make(chan struct{})

	go func() {
		<-signalCtx.Done()
		defer close(shutdownDone)
		fmt.Println("Stopping web and admin servers...")

		if err := server.Shutdown(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "HTTP shutdown:", err)
		}

		grpcServer.GracefulStop()
	}()

	fmt.Println("Starting web server on", listenAddr)
	err = server.Start(lis)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	if signalCtx.Err() != nil {
		<-shutdownDone
	}
	return nil
}
