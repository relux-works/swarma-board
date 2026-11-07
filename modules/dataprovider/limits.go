package dataprovider

import (
	"context"
	"time"
)

const (
	MaxElements                      = 10000
	MaxSnapshotBytes           int64 = 128 << 20
	MaxProvenanceCount               = 10000
	MaxProvenanceBytes         int64 = 8 << 20
	MaxProvenanceNameBytes           = 100
	MaxResourceCount                 = 10000
	MaxResourceBytes           int64 = 32 << 20
	MaxScopeResourceBytes      int64 = 256 << 20
	MaxChangeSetBytes          int64 = 512 << 20
	MaxEventNDJSONBytes        int64 = 256 << 20
	EventWarningBytes          int64 = 241591910
	MaxCheckpointBytes         int64 = 1024 << 20
	MaxNativeV2Entries               = 20002
	MaxNativeV2ManifestBytes   int64 = 8 << 20
	MaxNativeV2ElementsBytes   int64 = 128 << 20
	MaxNativeV2ExpandedBytes   int64 = 512 << 20
	MaxNativeV2CompressedBytes int64 = 640 << 20
	ReadTimeout                      = 30 * time.Second
	UpdateTimeout                    = 300 * time.Second
	CheckpointTimeout                = 300 * time.Second
	LockTimeout                      = 2 * time.Second
	CloseTimeout                     = 330 * time.Second
	RecoveryTimeout                  = 600 * time.Second
)

// BoundedContext starts the budget at admission. For streaming operations retain
// this context and cancel function until the entire handle lifetime ends; do
// not defer cancel in the acquisition method. A caller's earlier deadline wins.
// These common caps require future workload qualification; they are not timing
// measurements or a way to interrupt an arbitrary blocking caller Reader.
func BoundedContext(parent context.Context, cap time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, cap)
}
