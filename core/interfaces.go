package core

import "context"

// SegmentEncoder abstracts the ffmpeg operations required by the QP search loop.
// Implementations bridge to concrete encoder backends (e.g. ffmpeg libx265, NVENC)
// or to test mocks that return predetermined VMAF results.
type SegmentEncoder interface {
	// Name returns the encoder identifier used for cache filenames and logging.
	Name() string
	// QPRange returns the valid QP range for this encoder.
	// The second return value is false if the encoder is not supported.
	QPRange() (min, max int, found bool)
	// Encode produces an encoded segment at the given QP.
	Encode(ctx context.Context, input, output string, qp int, stream VideoStream,
		progress func(ProgressStats), debug func(string), runtimeError func(error)) error
	// ComputeVMAF calculates VMAF between a reference and a distorted segment.
	ComputeVMAF(ctx context.Context, reference, distorted string, stream VideoStream,
		progress func(ProgressStats), debug func(string), runtimeError func(error)) (VMAFStats, error)
	// ProbeStream extracts video stream information from a media file.
	ProbeStream(ctx context.Context, path string, progress func(int64),
		debug func(string), runtimeError func(error)) (VideoStream, error)
}
