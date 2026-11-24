// Package transnetv2 is the Go side of sptenc-detector-transnetv2, the helper executable running
// TransNetV2, a neural network finding shot boundaries: it locates the helper (Helper), checks its
// version (GetVersion), runs a detection on frames fed by the caller (Detect) and turns the
// probabilities it returns into cuts (Cuts). pipeline.DetectScenesTransNetV2 is its caller.
//
// # THE PROTOCOL (version 1)
//
// This is the specification the helper is written against.
//
//   - `sptenc-detector-transnetv2 version` prints one JSON object on one line of stdout and exits 0:
//     {"protocol":1,"detector":"transnetv2","version":"<helper version>","model":"<model file name>","model_sha256":"<hex>"}
//   - `sptenc-detector-transnetv2 detect` reads raw frames on stdin until end of input: RGB24, 48
//     wide by 27 high, 3,888 bytes each, in order. It writes to stdout one line per frame, in order:
//     that frame's probability of being the last frame of a shot, as decimal text of a 32-bit float
//     between 0 and 1. Lines may come late (the model works on windows of 100 frames) but every
//     line is written before it exits 0. stderr carries diagnostics only. Any non-zero exit is a
//     failure.
package transnetv2

import (
	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/processpriority"
)

const (
	// ProtocolVersion is the version of the protocol this package speaks, which the helper states
	// in its version line.
	ProtocolVersion = 1
	// DetectorName is the detector the helper states in its version line.
	DetectorName = "transnetv2"
	// FrameWidth and FrameHeight are the size of the frames the helper reads, in pixels.
	FrameWidth  = 48
	FrameHeight = 27
	// FrameSize is the size of one frame in bytes: RGB24, 3 bytes per pixel, no padding.
	FrameSize = FrameWidth * FrameHeight * 3
)

var (
	// ProcessPriority is the priority the helper runs at, as ffmpeg and mkvpropedit do.
	ProcessPriority = processpriority.BelowNormal
)

func getPrintableCMDLine(program string, args []string) string {
	return shellescape.Quote(program) + " " + shellescape.QuoteCommand(args)
}
