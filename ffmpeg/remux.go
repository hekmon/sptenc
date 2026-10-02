package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hekmon/processpriority"
)

// FFMEGTags is a list of FFMPEG tags (cli flag + value)
type FFMEGTags []string

// FLAC encoding constants
const (
	FLACExactRiceParams = 1  // Use exact Rice parameters for lossless
	FLACCompressionMin  = 0  // Minimum compression level
	FLACCompressionMax  = 12 // Maximum compression level
)

// RemuxSwapVideoConfig holds the parameters for a video swap remux operation.
type RemuxSwapVideoConfig struct {
	// Input
	OriginalFile string
	NewVideoFile string
	// Output
	// OutputFilePath must use the .mkv extension. Matroska is required because
	// it is the most permissive container for stream copy and it carries HDR
	// container metadata (Colour elements, MaxCLL, MaxFALL) that other containers
	// lack or encode differently.
	OutputFilePath string
	EncodeToFLAC   bool
	Tags           FFMEGTags // tags to add, can be nil
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // non fatal errors
	FFMPEGStatsReport func(stats ProgressStats)
}

// RemuxSwapVideo replaces the video stream of OriginalFile with the video from
// NewVideoFile while copying all other streams (audio, subtitles, data,
// attachments). If EncodeToFLAC is true, audio is re-encoded to FLAC with
// maximum compression; otherwise it is copied as-is.
func RemuxSwapVideo(ctx context.Context, config RemuxSwapVideoConfig) (err error) {
	// Validate inputs
	if config.OriginalFile == "" {
		return errors.New("original file path cannot be empty")
	}
	if config.NewVideoFile == "" {
		return errors.New("new video file path cannot be empty")
	}
	if config.OutputFilePath == "" {
		return errors.New("output file path cannot be empty")
	}
	// Matroska is required because:
	// - It is the most permissive container for stream copy (handles arbitrary codecs, attachments, subtitles).
	// - It carries HDR container metadata (Colour elements, MaxCLL, MaxFALL) that other containers lack or encode differently.
	if filepath.Ext(config.OutputFilePath) != ".mkv" {
		return errors.New("output file path must have a .mkv extension")
	}
	// Extract container-level color metadata from the original file so we can
	// propagate it onto the copied video stream. With -c:v copy ffmpeg copies
	// encoded packets unchanged, but the output stream's codecpar (which the
	// muxer uses to write container-level color tags / Matroska Colour elements)
	// is inherited from the stream actually mapped. The mapped stream may have
	// left these codecpar fields empty or wrong even when its bitstream SEIs are
	// correct, because encoders write SEIs into the encoded frames while muxers
	// write container tags from codecpar, and the two are independent. By probing
	// the original file and passing output-side -color_* flags we override the
	// mapped stream's codecpar with the original's authoritative values. This does
	// not affect or create bitstream-level SEIs.
	// The color range and the matrix are the exception: they are taken from the new video (see
	// RemuxColorRange and RemuxColorSpace).
	var (
		originalVideo  *FFProbeBinaryStream
		newVideo       *FFProbeBinaryStream
		colorTransfer  string
		colorPrimaries string
	)
	if originalStats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{
		Path:         config.OriginalFile,
		Debug:        config.Debug,
		RuntimeError: config.RuntimeError,
	}); err == nil {
		if originalVideo = originalStats.VideoTrack(); originalVideo != nil {
			colorTransfer = originalVideo.ColorTransfer
			colorPrimaries = originalVideo.ColorPrimaries
		}
	} else if config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("failed to probe original file for color metadata: %w", err))
	}
	if newStats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{
		Path:         config.NewVideoFile,
		Debug:        config.Debug,
		RuntimeError: config.RuntimeError,
	}); err == nil {
		newVideo = newStats.VideoTrack()
	} else if config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("failed to probe the new video file for its color range and matrix: %w", err))
	}
	colorRange := RemuxColorRange(originalVideo, newVideo)
	colorSpace := RemuxColorSpace(originalVideo, newVideo)
	// Prepare
	args := []string{
		"-y", "-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
	}
	// Inputs
	args = append(args,
		"-i", config.OriginalFile,
		"-i", config.NewVideoFile,
	)
	//// stream mapping: video from the encoded merge, everything else from the original
	args = append(args,
		"-map", "1:v", // video from encoded merge
		"-map", "0", // all streams from original...
		"-map", "-0:v", // ...except its video streams
	)
	//// codecs
	args = append(args,
		"-c:v", "copy",
		"-c:s", "copy",
		"-c:d", "copy",
		"-c:t", "copy",
	)
	if config.EncodeToFLAC {
		args = append(args, "-c:a", "flac", "-compression_level", strconv.Itoa(FLACCompressionMax), "-exact_rice_parameters", strconv.Itoa(FLACExactRiceParams))
	} else {
		args = append(args, "-c:a", "copy")
	}
	//// container-level options (muxer behavior, metadata, color signaling)
	if colorRange != "" {
		args = append(args, "-color_range:v:0", colorRange)
	}
	if colorSpace != "" {
		args = append(args, "-colorspace:v:0", colorSpaceOptionValue(colorSpace))
	}
	if colorTransfer != "" {
		args = append(args, "-color_trc:v:0", colorTransfer)
	}
	if colorPrimaries != "" {
		args = append(args, "-color_primaries:v:0", colorPrimaries)
	}
	args = append(args,
		"-max_interleave_delta", "0", // avoid '[matroska] Starting new cluster due to timestamp'
	)
	args = append(args, config.Tags...)
	//// end with output
	args = append(args, config.OutputFilePath)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Remux with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stdout pipe: %w", err)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stderr pipe: %w", err)
		return
	}
	// Start program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	// Start progress monitoring (after cmd.Start to avoid goroutine leak on error)
	progressDone := make(chan struct{})
	go func() {
		standardProgress(stdoutPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
	}()
	stderrDone := make(chan struct{})
	go func() {
		stderrForwarder(stderrPipe, config.RuntimeError)
		close(stderrDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	<-stderrDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	return
}

// RemuxColorRange returns the color range RemuxSwapVideo declares for the video it takes from the
// new video file, given the video streams of both files (nil when a file has none or could not be
// probed): the range of the new video when it declares one, the range of the original's otherwise.
//
// # WHY THE NEW VIDEO'S RANGE, NOT THE ORIGINAL'S
//
// The range tells how to read the pixel values: limited ("tv": black at 64, white at 940 in
// 10 bits) or full ("pc": 0 and 1023). It describes a stream, and the stream is the new video's,
// whose pixels are not always in the original's range: a full range source is converted to limited
// range when the master is written, ffmpeg's FFV1 encoder declaring limited range only
// (color_ranges in libavcodec/ffv1enc.c). The encodes of the master are limited range, their first
// decoded frame says so, and declaring the original's range wrote "pc" in the container over them
// (measured with ffmpeg 9.0.2 on 8-bit H.264, 8-bit HEVC and 10-bit HEVC full range sources, with
// libx265, hevc_nvenc, libsvtav1 and av1_nvenc).
//
// Only the container is wrong then, the video stream is the same, and what it misleads depends on
// what reads it. ffmpeg's HEVC and AV1 decoders take the range from the stream (for HEVC, limited
// by default when the stream declares none, as with hevc_nvenc): they rendered those outputs the
// same either way, while MediaInfo (24.01) reported them as full range. ffmpeg's H.264 decoder
// takes it from the container when the stream declares none: a limited range H.264 video grafted
// onto a full range original by the remux command was rendered washed out, the luma of the
// rendered picture spanning 26 to 217 instead of 15 to 228 (out of 255).
//
// Writing limited range whenever the original is full range was rejected: read from the new video,
// the range is right whatever the new video went through. The segments of a pre-split directory
// were not necessarily cut from a master, the remux command is given any new video, and ffmpeg
// versions before 7.1, in which the FFV1 encoder declares no range, were not checked.
//
// # WHY THE FULL RANGE IS NOT KEPT INSTEAD
//
// Keeping a full range source in full range down to the output, which would have kept the
// original's range right, was not attempted: it changes the frames every encoder and every VMAF
// computation are given, not the remux. The conversion loses nothing from 8 bits (each level of
// the source gets a 10-bit level of its own), and little from 10 bits (the 1024 luma levels of the
// source share the 877 of the limited range).
//
// # EDGE CASES
//
//   - The new video declares no range: the original's is declared, as before. ffprobe reports the
//     range of the container, else the one of the stream, so it takes a stream without any range
//     in a container without any, such as H.264 without video signal type (HEVC without it is
//     read as limited range, its default, and AV1 always carries one). The encodes of the master
//     declare one with the four encoders above: ffmpeg declares the master limited range even
//     when the source declares no range.
//   - The remux command is given any new video: a full range one grafted onto a limited range
//     original is declared full range, as its stream is, where the original's range used to be.
func RemuxColorRange(originalVideo, newVideo *FFProbeBinaryStream) string {
	if newVideo != nil && (newVideo.ColorRange == "tv" || newVideo.ColorRange == "pc") {
		return newVideo.ColorRange
	}
	if originalVideo != nil {
		return originalVideo.ColorRange
	}
	return ""
}

// RemuxColorSpace returns the matrix (colorspace) RemuxSwapVideo declares for the video it takes
// from the new video file, given the video streams of both files (nil when a file has none or could
// not be probed), as ffprobe names it: the matrix of the new video when it declares one, the
// original's otherwise, provided both hold the same kind of pictures, RGB or YUV.
//
// # WHY THE NEW VIDEO'S MATRIX, NOT THE ORIGINAL'S
//
// The matrix tells how to turn the YUV of a stream back into RGB: like the range (see
// RemuxColorRange), it describes a stream, and the stream is the new video's. An RGB source
// declares the identity ("gbr" for ffprobe), and its master is converted to YUV with a matrix the
// master and its encodes declare (see YUVMatrix). Declaring the original's identity over the output
// declared a YUV video as RGB, and did not get that far: ffmpeg's -colorspace option names the
// identity "rgb" and refused "gbr", so every encode of an RGB source failed at its last step, the
// remux, after the whole search (measured with libx265, hevc_nvenc, libsvtav1 and av1_nvenc), and
// so did batchsearch and the remux command given an RGB original.
//
// A YUV source keeps its matrix down to the new video: the master keeps the one of the frames the
// source decodes to (checked on 8-bit 4:2:0, 10-bit 4:2:0 and full range 10-bit 4:4:4 sources
// declaring BT.709), and the encodes keep the master's (checked with the four encoders above). The
// output declares the same matrix as before for those.
//
// # EDGE CASES
//
//   - The new video declares no matrix: the original's is declared, as before, when both hold the
//     same kind of pictures, judged by their pixel formats. An RGB original's matrix is never
//     declared over a YUV video, nor a YUV original's over an RGB one: no matrix then, rather than
//     a wrong one. A new video that could not be probed counts as YUV, what sptenc encodes.
//   - ffprobe leaves an unspecified matrix out, "unknown" and "reserved" declare nothing either.
//   - The identity is returned as ffprobe names it, "gbr": RemuxSwapVideo hands it to ffmpeg as
//     "rgb" (see colorSpaceOptionValue).
func RemuxColorSpace(originalVideo, newVideo *FFProbeBinaryStream) string {
	declared := func(matrix string) bool {
		return matrix != "" && matrix != "unknown" && matrix != "reserved"
	}
	if newVideo != nil && declared(newVideo.ColorSpace) {
		return newVideo.ColorSpace
	}
	newRGB := newVideo != nil && IsRGBPixelFormat(newVideo.PixFmt)
	if originalVideo != nil && declared(originalVideo.ColorSpace) && IsRGBPixelFormat(originalVideo.PixFmt) == newRGB {
		return originalVideo.ColorSpace
	}
	return ""
}

// colorSpaceOptionValue returns the value ffmpeg's -colorspace option takes for a matrix as ffprobe
// names it: the option knows the identity of RGB streams as "rgb", and refuses the "gbr" ffprobe
// prints. It takes the other names ffprobe prints for a matrix as they are, but "reserved", which
// RemuxColorSpace never returns (checked with ffmpeg 9.0.2).
func colorSpaceOptionValue(matrix string) string {
	if matrix == "gbr" {
		return "rgb"
	}
	return matrix
}
