# TritonTube

[![CI](https://github.com/Leon-CYL/TritonTube/actions/workflows/ci.yml/badge.svg)](https://github.com/Leon-CYL/TritonTube/actions/workflows/ci.yml)
![Core coverage](https://img.shields.io/badge/core%20coverage-43.0%25-yellow)
[![Go](https://img.shields.io/badge/Go-1.24.1-00ADD8?logo=go&logoColor=white)](https://go.dev/)

TritonTube is a distributed video-streaming application written in Go. It
transcodes uploaded videos into MPEG-DASH content with FFmpeg, distributes
manifests and segments across filesystem-backed gRPC storage nodes using
consistent hashing, and stores video metadata in an etcd cluster. A Redis
cache accelerates popular segment reads, while bounded concurrent batching
improves uploads and storage-node migration.

## Performance

### Baseline vs. optimized file operations

| Operation | Baseline | Optimized | Improvement |
| --- | --- | --- | --- |
| Files Upload | 2,509.92 ms | 1,761.00 ms | 1.43x faster |
| Files Migration | 1,182.80 ms | 821.44 ms | 1.44x faster |

### Video segment cache

Random-access workload across three 15-minute videos: 80% of requests target
the first 20% of each video's segments and 20% target the remaining segments.
Each result is a 60-second `wrk` run. Warm-cache runs use a 30-second warm-up
with the same concurrency before measurement.

| Cache state | Concurrency | Requests/sec | p50 latency | p99 latency | Timeouts | Cache hit rate |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Baseline | 1 | 66.67 | 14.08 ms | 35.91 ms | 0 | N/A |
| Baseline | 10 | 93.62 | 101.31 ms | 252.85 ms | 0 | N/A |
| Baseline | 50 | 86.65 | 520.19 ms | 1010 ms | 0 | N/A |
| Cold cache | 1 | 87.97 | 9.70 ms | 39.45 ms | 0 | 83.73% |
| Cold cache | 10 | 110.43 | 86.74 ms | 186.40 ms | 0 | 88.62% |
| Cold cache | 50 | 115.93 | 400.92 ms | 982.23 ms | 0 | 91.43% |
| Warm cache | 1 | 95.69 | 9.30 ms | 25.49 ms | 0 | 86.87% |
| Warm cache | 10 | 111.79 | 87.82 ms | 140.56 ms | 0 | 93.86% |
| Warm cache | 50 | 116.41 | 398.54 ms | 974.88 ms | 0 | 98.12% |

## Architecture

```mermaid
flowchart LR
    U["Browser / HTTP client"] --> W["Web service"]
    W --> F["FFmpeg"]

    subgraph E["etcd metadata cluster (3 members)"]
        E1["etcd node 1"]
        E2["etcd node 2"]
        E3["etcd node 3"]
        E1 <--> E2
        E2 <--> E3
        E3 <--> E1
    end

    W --> E1
    W --> E2
    W --> E3
    W --> H["Consistent-hash ring"]
    H --> S1["Storage node A"]
    H --> S2["Storage node B"]
    H --> S3["Storage node C"]
    A["Admin CLI"] --> G["Admin gRPC service"]
    G --> H
```

The web service hashes each `videoID/filename` key and selects the first storage node clockwise on the ring. Adding a node copies only the files reassigned to it. Removing a node copies its files to the next owner before publishing the updated ring.

Uploads use a bounded pool of at most 64 workers. Each worker reads up to four
DASH files and sends them with batch gRPC writes, grouped by their storage-node
owner. Node migration uses up to eight concurrent workers with bounded batches
of four files for both `ReadFiles` and `WriteFiles`. Small batches keep requests
below the configured 16 MiB gRPC message limit while concurrency overlaps file
I/O, protobuf processing, and network transfer.

## Requirements

- [Go 1.24.1 or newer](https://go.dev/dl/)
- [FFmpeg](https://ffmpeg.org/)
- [etcd](https://etcd.io/) for host-based local runs
- [Docker](https://docs.docker.com/engine/install/) with Docker Compose for containerized runs
- `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` when regenerating protobuf code

On macOS with Homebrew:

```bash
brew install ffmpeg etcd protobuf
```

Install the Go protobuf generators:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
export PATH="$PATH:$(go env GOPATH)/bin"
```

## Docker commands

Docker Compose starts Redis, three etcd members, three persistent storage nodes,
and the web service. Ports `8080` and `6379` are published to the host.

### Start the application

Build the images and start the complete application in the background:

```bash
docker compose up --build --detach
```

Open [http://localhost:8080](http://localhost:8080).

View container status and follow logs:

```bash
docker compose ps
docker compose logs --follow
```

### Local tests

```bash
make test
make race
```

Run a specific package:

```bash
go test -v ./internal/storage
go test -v ./internal/web
```