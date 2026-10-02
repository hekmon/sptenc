package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/hekmon/processpriority"
)

// masterPixelFormat is the pixel format of the master, and of every encode: 10-bit 4:2:0.
const masterPixelFormat = "yuv420p10le"

// FFV1VideoMasterConfig holds the configuration for FFV1VideoMaster().
type FFV1VideoMasterConfig struct {
	// Input
	InputFilePath string
	// RGBToYUV is the matrix an RGB input is converted with (see YUVMatrix), empty for a YUV one.
	RGBToYUV YUVMatrix
	// Hardware decode (caller decides based on encoder choice and codec compatibility)
	NVDec           bool   // use NVDEC for hardware-accelerated decoding
	NVDevice        int    // NVIDIA GPU index, see CUDADefaultDevice
	VAAPIDec        bool   // use VA-API for hardware-accelerated decoding
	VAAPIDevice     string // DRM render node, see VAAPIDefaultDevice
	D3D12Dec        bool   // use D3D12VA for hardware-accelerated decoding
	D3D12Device     int    // Direct3D 12 adapter index, see D3D12VADefaultDevice
	VideoToolboxDec bool   // use VideoToolbox for hardware-accelerated decoding
	// Output: the master file, or the master cut into its segments within SegmentsDir, at
	// ScenesFrames (see SegmentConfig.ScenesFrames): the same segments Segment would cut from
	// the master file. Exactly one of OutputFilePath and SegmentsDir must be set.
	OutputFilePath string
	SegmentsDir    string
	ScenesFrames   []int
	// Reporting
	Debug        func(msg string)
	RuntimeError func(err error) // non fatal errors
	StatsReport  func(stats ProgressStats)
}

// FFV1VideoMaster encodes the input video file to FFV1 with intra frames and 10-bit YUV420P,
// into one file or already cut into its segments (SegmentsDir): the segment muxer cuts the
// packets of the encoder exactly as Segment cuts those of the master file, same packets and same
// timestamps (see TestFFV1VideoMasterSegments), only the duration a segment's container declares
// can end 1 ms later. Only the first video stream is kept; all other streams are dropped.
// Intra frames only is what lets Segment cut at any frame: see Segment for why the source can
// not be cut directly. An RGB input is converted with the matrix of RGBToYUV (see YUVMatrix), the
// chroma of a full chroma YUV input subsampled with the left siting (see FullChromaToMasterFilter).
func FFV1VideoMaster(ctx context.Context, config FFV1VideoMasterConfig) (err error) {
	// Validate inputs
	if config.InputFilePath == "" {
		return errors.New("input path cannot be empty")
	}
	if config.RGBToYUV != "" && !config.RGBToYUV.Valid() {
		return fmt.Errorf("invalid matrix %q to convert RGB to YUV with", config.RGBToYUV)
	}
	if (config.OutputFilePath == "") == (config.SegmentsDir == "") {
		return errors.New("exactly one of the output file path and the segments directory must be set")
	}
	if config.SegmentsDir != "" {
		if err = checkScenesFrames(config.ScenesFrames); err != nil {
			return
		}
	}
	// Apply defaults
	if config.NVDevice == 0 {
		config.NVDevice = CUDADefaultDevice
	}
	if config.VAAPIDevice == "" {
		config.VAAPIDevice = VAAPIDefaultDevice
	}
	if config.D3D12Device == 0 {
		config.D3D12Device = D3D12VADefaultDevice
	}
	// Prepare arguments
	args := []string{
		"-y", "-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
	}
	// Hardware decode paths
	if config.NVDec {
		args = append(args, "-hwaccel", "cuda")
		if config.NVDevice >= 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(config.NVDevice))
		}
	} else if config.VAAPIDec {
		args = append(args, "-hwaccel", "vaapi")
		if config.VAAPIDevice != "" {
			args = append(args, "-vaapi_device", config.VAAPIDevice)
		}
	} else if config.D3D12Dec {
		args = append(args, "-hwaccel", "d3d12va")
		if config.D3D12Device >= 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(config.D3D12Device))
		}
	} else if config.VideoToolboxDec {
		args = append(args, "-hwaccel", "videotoolbox")
	}
	args = append(args,
		"-i", config.InputFilePath,
		"-map", "0:v:0", // we only want the video stream
	)
	if config.RGBToYUV != "" {
		args = append(args, "-vf", RGBToYUVFilter(config.RGBToYUV))
	} else if stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: config.InputFilePath}); err == nil &&
		stats.VideoTrack() != nil && IsFullChromaYUV(stats.VideoTrack().PixFmt) {
		args = append(args, "-vf", FullChromaToMasterFilter)
	}
	args = append(args,
		"-c:v", "ffv1", // encoded as ffv1
		"-g", "1", // with every frame self contained
		"-pix_fmt", masterPixelFormat, // in 10bits output
		"-fps_mode", "passthrough", // preserve original timestamps
	)
	if config.SegmentsDir != "" {
		args = append(args, segmentsOutputArgs(config.ScenesFrames, config.SegmentsDir)...)
	} else {
		args = append(args, config.OutputFilePath)
	}
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
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
		standardProgress(stdoutPipe, config.StatsReport, config.RuntimeError)
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
