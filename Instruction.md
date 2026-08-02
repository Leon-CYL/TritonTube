# TritonTube

## Performance

Sequential for loop:

1. Write File Performance: 475 DASH files for video 1 in 29.48 second, average 61.38ms

2. Migrate File Performance: 267 DASH files for video 1 in 16.98 second, average 63.60ms

3. Read File Performance: average 58ms

ThreadPool (Write Files):

1. 32 workers:
   2025/07/15 13:45:58 Uploaded 475 DASH files for video2 in 1.211037125s
   2025/07/15 13:45:58 Average write time per file: 2.549551ms

2. 64 workers:
   2025/07/15 13:47:30 Uploaded 475 DASH files for video3 in 800.514666ms
   2025/07/15 13:47:30 Average write time per file: 1.685294ms

3. 128 workers:
   2025/07/15 13:50:42 Uploaded 475 DASH files for video4 in 5.85389025s
   2025/07/15 13:50:42 Average write time per file: 12.323979ms

Batch gRPC and RocksDB(Migrating Files):
1. Migrate File Performance: 267 DASH files for video 1 in 315 ms

