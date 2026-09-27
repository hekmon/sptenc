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
	// ComputeVMAF measures a distorted segment against its reference in one pass: the VMAF
	// score the thresholds gate, the banding the encode added, or both (see VMAFMeasures). What
	// was not measured is returned zero.
	ComputeVMAF(ctx context.Context, reference, distorted string, stream VideoStream, measures VMAFMeasures,
		progress func(ProgressStats), debug func(string), runtimeError func(error)) (VMAFStats, BandingStats, error)
	// ProbeStream extracts video stream information from a media file: its metadata only, no
	// frame is decoded (NbReadFrames is not set, see CountFrames).
	ProbeStream(ctx context.Context, path string, debug func(string), runtimeError func(error)) (VideoStream, error)
	// CountFrames decodes the whole video stream of a media file to count its frames exactly.
	CountFrames(ctx context.Context, path string, progress func(ProgressStats),
		debug func(string), runtimeError func(error)) (int, error)
}
