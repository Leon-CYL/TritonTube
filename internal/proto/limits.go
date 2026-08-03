package proto

// MaxMessageSize permits individual DASH segments larger than gRPC's 4 MiB
// default while keeping a bounded limit for single-file transfers.
const MaxMessageSize = 16 * 1024 * 1024
