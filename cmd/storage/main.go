package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"tritontube/internal/proto"
	"tritontube/internal/storage"

	"google.golang.org/grpc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "storage:", err)
		os.Exit(1)
	}
}

func run() error {
	host := flag.String("host", "localhost", "Host address for the server")
	port := flag.Int("port", 8090, "Port number for the server")
	flag.Parse()

	// Validate arguments
	if *port <= 0 {
		return errors.New("port number must be positive")
	}
	if flag.NArg() < 1 {
		return errors.New("usage: storage [OPTIONS] <baseDir>: base directory is required")
	}
	baseDir := flag.Arg(0)

	fmt.Println("Starting storage server...")
	fmt.Printf("Host: %s\n", *host)
	fmt.Printf("Port: %d\n", *port)
	fmt.Printf("Base Directory: %s\n", baseDir)

	// use gRPC to start the server

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(proto.MaxMessageSize),
		grpc.MaxSendMsgSize(proto.MaxMessageSize),
	)

	server := storage.NewStorageServer(baseDir)

	if server == nil {
		return errors.New("storage server initialization failed")
	}

	proto.RegisterVideoContentStorageServiceServer(grpcServer, server)

	lis, err := net.Listen("tcp", *host+":"+strconv.Itoa(*port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer lis.Close()

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	go func() {
		<-signalCtx.Done()
		fmt.Println("Stopping storage server...")
		grpcServer.GracefulStop()
	}()

	fmt.Printf("Storage server %s is running...\n", *host+":"+strconv.Itoa(*port))
	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
