package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
)

// EncoderAdapter bridges ffmpeg concrete operations to the core.SegmentEncoder interface.
type EncoderAdapter struct {
	Encoder           ffmpeg.Encoder
	NVIDIAGPUIndex    int
	VAAPIRendererPath string
	D3D12VAGPUIndex   int
	VMAFModel         ffmpeg.VMAFModel
	// Hardware decoder of the run (see ffmpeg.ResolveHWDecoder): applied by the ffmpeg functions
	// to every input whose codec it can decode, software decode for the others (FFV1 segments).
	HWDecoder ffmpeg.HWDecoderConfig
}

// Name returns the encoder identifier.
func (e *EncoderAdapter) Name() string {
	return string(e.Encoder)
}

// QPRange returns the valid QP range for the configured encoder.
func (e *EncoderAdapter) QPRange() (min, max int, found bool) {
	return ffmpeg.GetEncoderQPRange(e.Encoder)
}

// Encode produces an encoded segment at the given QP using the configured ffmpeg encoder.
func (e *EncoderAdapter) Encode(ctx context.Context, input, output string, qp int, stream core.VideoStream,
	progress func(core.ProgressStats), debug func(string), runtimeError func(error)) error {
	start := time.Now()
	var err error
	switch e.Encoder {
	// HEVC
	case ffmpeg.HEVCEncoderLibx265:
		err = ffmpeg.HEVCLibx265EncodeQP(ctx, ffmpeg.HEVCLibx265EncodeQPConfig{
			Input:             input,
			Preset:            ffmpeg.Libx265PresetSlow,
			Quantization:      qp,
			Output:            output,
			Debug:             debug,
			RuntimeError:      runtimeError,
			FFMPEGStatsReport: adaptProgress(progress),
		})
	case ffmpeg.HEVCEncoderNVEnc:
		err = ffmpeg.HEVCNVEncEncodeQP(ctx, ffmpeg.HEVCNVEncEncodeQPConfig{
			Input:             input,
			Device:            e.NVIDIAGPUIndex,
			Preset:            ffmpeg.NVEncPresetP7,
			Quantization:      qp,
			Output:            output,
			Debug:             debug,
			RuntimeError:      runtimeError,
			FFMPEGStatsReport: adaptProgress(progress),
		})
	case ffmpeg.HEVCEncoderVAAPI:
		err = ffmpeg.HEVCVAAPIEncodeQP(ctx, ffmpeg.HEVCVAAPIEncodeQPConfig{
			Input:             input,
			Device:            e.VAAPIRendererPath,
			Quantization:      qp,
			Output:            output,
			Debug:             debug,
			RuntimeError:      runtimeError,
			FFMPEGStatsReport: adaptProgress(progress),
		})
	case ffmpeg.HEVCEncoderD3D12VA:
		err = ffmpeg.HEVCD3D12VAEncodeQP(ctx, ffmpeg.HEVCD3D12VAEncodeQPConfig{
			Input:             input,
			Device:            e.D3D12VAGPUIndex,
			Quantization:      qp,
			Output:            output,
			Debug:             debug,
			RuntimeError:      runtimeError,
			FFMPEGStatsReport: adaptProgress(progress),
		})
	case ffmpeg.HEVCEncoderVideoToolbox:
		err = ffmpeg.HEVCVideoToolboxEncodeQP(ctx, ffmpeg.HEVCVideoToolboxEncodeQPConfig{
			Input:             input,
			Quantization:      qp,
			Output:            output,
			Debug:             debug,
			RuntimeError:      runtimeError,
			FFMPEGStatsReport: adaptProgress(progress),
		})
	// AV1
	case ffmpeg.AV1EncoderLibaom:
		err = ffmpeg.AV1LibaomEncodeQP(ctx, ffmpeg.AV1LibaomEncodeQPConfig{
			Input:             input,
			CPUUsed:           ffmpeg.AV1LibaomCPUUsedDefault,
			Quantization:      qp,
			Output:            output,
			Debug:             debug,
			RuntimeError:      runtimeError,
			FFMPEGStatsReport: adaptProgress(progress),
		})
	case ffmpeg.AV1EncoderSVTAV1:
		err = ffmpeg.AV1SVTAV1EncodeQP(ctx, ffmpeg.AV1SVTAV1EncodeQPConfig{
			Input:             input,
			Preset:            ffmpeg.AV1SVTAV1PresetDefault,
			Quantization:      qp,
			Output:            output,
			Debug:             debug,
			RuntimeError:      runtimeError,
			FFMPEGStatsReport: adaptProgress(progress),
		})
	case ffmpeg.AV1EncoderNVEnc:
		err = ffmpeg.AV1NVEncEncodeQP(ctx, ffmpeg.AV1NVEncEncodeQPConfig{
			Input:             input,
			Device:            e.NVIDIAGPUIndex,
			Preset:            ffmpeg.NVEncPresetP7,
			Quantization:      qp,
			Output:            output,
			Debug:             debug,
			RuntimeError:      runtimeError,
			FFMPEGStatsReport: adaptProgress(progress),
		})
	case ffmpeg.AV1EncoderVAAPI:
		err = ffmpeg.AV1VAAPIEncodeQP(ctx, ffmpeg.AV1VAAPIEncodeQPConfig{
			Input:             input,
			Device:            e.VAAPIRendererPath,
			Quantization:      qp,
			Output:            output,
			Debug:             debug,
			RuntimeError:      runtimeError,
			FFMPEGStatsReport: adaptProgress(progress),
		})
	default:
		return fmt.Errorf("unsupported encoder: %q", string(e.Encoder))
	}
	if err == nil && debug != nil {
		debug(fmt.Sprintf("Segment encoded in %s", time.Since(start).Round(time.Second)))
	}
	return err
}

// ComputeVMAF calculates VMAF between a reference and a distorted segment using ffmpeg libvmaf.
func (e *EncoderAdapter) ComputeVMAF(ctx context.Context, reference, distorted string, stream core.VideoStream,
	progress func(core.ProgressStats), debug func(string), runtimeError func(error)) (core.VMAFStats, error) {
	report, err := ffmpeg.VMAFCompute(ctx, ffmpeg.VMAFComputeConfig{
		ReferencePath:     reference,
		DistortedPath:     distorted,
		InputFrameRate:    stream.RFrameRate,
		ReportPath:        distorted + "_vmaf.json",
		Model:             e.VMAFModel,
		HWDecoderConfig:   e.HWDecoder,
		Debug:             debug,
		RuntimeError:      runtimeError,
		FFMPEGStatsReport: adaptProgress(progress),
	})
	if err != nil {
		return core.VMAFStats{}, err
	}
	stats := report.GetStats()
	return core.VMAFStats{
		Version:      stats.Version,
		Minimum:      stats.Minimum,
		Percentile1:  stats.Percentile1,
		Percentile5:  stats.Percentile5,
		Percentile10: stats.Percentile10,
		Percentile25: stats.Percentile25,
		Median:       stats.Median,
		HarmonicMean: stats.HarmonicMean,
		Mean:         stats.Mean,
		Maximum:      stats.Maximum,
		CAMBIMean:    stats.CAMBIMean,
		CAMBIMax:     stats.CAMBIMax,
	}, nil
}

// ProbeStream extracts video stream information from a media file using ffprobe: metadata
// only, no frame is decoded (see CountFrames).
func (e *EncoderAdapter) ProbeStream(ctx context.Context, path string, debug func(string), runtimeError func(error)) (core.VideoStream, error) {
	stats, err := ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
		Path:         path,
		Debug:        debug,
		RuntimeError: runtimeError,
	})
	if err != nil {
		return core.VideoStream{}, err
	}
	video := stats.VideoTrack()
	if video == nil {
		return core.VideoStream{}, fmt.Errorf("no video track found in %s", path)
	}
	return core.VideoStream{
		NbFrames:   video.NbFrames,
		RFrameRate: video.RFrameRate,
		Height:     video.Height,
		Duration:   stats.Format.Duration,
	}, nil
}

// CountFrames decodes the whole video stream of a media file with ffmpeg to count its frames
// exactly, with the hardware decoder of the run when the codec of the file allows it.
func (e *EncoderAdapter) CountFrames(ctx context.Context, path string, progress func(core.ProgressStats),
	debug func(string), runtimeError func(error)) (int, error) {
	frames, err := ffmpeg.CountFrames(ctx, ffmpeg.CountFramesConfig{
		Path:              path,
		HWDecoderConfig:   e.HWDecoder,
		Debug:             debug,
		RuntimeError:      runtimeError,
		FFMPEGStatsReport: adaptProgress(progress),
	})
	if err != nil {
		return 0, err
	}
	return frames.Nb, nil
}

// adaptProgress converts a core.ProgressStats callback to an ffmpeg.ProgressStats callback.
func adaptProgress(progress func(core.ProgressStats)) func(ffmpeg.ProgressStats) {
	if progress == nil {
		return nil
	}
	return func(ps ffmpeg.ProgressStats) {
		progress(core.ProgressStats{
			CurrentFrame: ps.CurrentFrame,
			FPS:          ps.FPS,
			Bitrate:      ps.Bitrate,
			TotalSize:    ps.TotalSize,
			Time:         ps.Time,
			Dup:          ps.Dup,
			Drop:         ps.Drop,
			Speed:        ps.Speed,
		})
	}
}
