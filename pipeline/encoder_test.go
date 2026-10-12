package pipeline

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
)

// TestEncoderAdapter_ComputeVMAF runs the real thing on the 8-bit-like steps of the dark gradient
// of BENCHMARKS.md (Synthetic clips): what the search asks for is what it gets, the gated score
// the adapter is given, and a banding pass leaves the VMAF report of the file alone.
func TestEncoderAdapter_ComputeVMAF(t *testing.T) {
	if _, err := exec.LookPath(ffmpeg.FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", ffmpeg.FFMPEGBinary, err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	clip := func(name, luma string) string {
		path := filepath.Join(dir, name)
		out, err := exec.Command(ffmpeg.FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y", "-f", "lavfi", "-i",
			"color=black:s=1920x1080:r=24:d=0.5,format=yuv420p10le,geq=lum='"+luma+"':cb=512:cr=512",
			"-c:v", "ffv1", path).CombinedOutput()
		if err != nil {
			t.Fatalf("failed to generate %s: %s\n%s", name, err, out)
		}
		return path
	}
	gradient := clip("gradient.mkv", "64+floor(X/12)")
	steps := clip("seg_000000_qp020.mkv", "64+4*floor(X/48)")
	stream := core.VideoStream{RFrameRate: "24"}
	round := func(v float64) float64 { return math.Round(v*100) / 100 }
	banding := core.BandingStats{AddedMean: 15.83, AddedMax: 15.83, SourceMean: 6.65, EncodeMean: 22.48}
	for _, tc := range []struct {
		score         ffmpeg.VMAFScore
		measures      core.VMAFMeasures
		vmaf          float64
		banding       core.BandingStats
		report, other string // the report written, and the one not
	}{
		{ffmpeg.VMAFScoreFidelity, core.VMAFMeasures{Score: true}, 95.19, core.BandingStats{}, "_vmaf.json", "_banding.json"},
		{ffmpeg.VMAFScoreOriginal, core.VMAFMeasures{Score: true}, 84.52, core.BandingStats{}, "_vmaf.json", "_banding.json"},
		{ffmpeg.VMAFScoreFidelity, core.VMAFMeasures{Banding: true}, 0, banding, "_banding.json", "_vmaf.json"},
		{ffmpeg.VMAFScoreFidelity, core.VMAFMeasures{Score: true, Banding: true}, 95.19, banding, "_vmaf.json", "_banding.json"},
		{ffmpeg.VMAFScoreOriginal, core.VMAFMeasures{Score: true, Banding: true}, 84.52, banding, "_vmaf.json", "_banding.json"},
	} {
		adapter := &EncoderAdapter{Encoder: ffmpeg.HEVCEncoderLibx265, VMAFModel: ffmpeg.VMAFModelFHD, VMAFModelCAMBI: true, VMAFScore: tc.score}
		for _, suffix := range []string{"_vmaf.json", "_banding.json"} {
			if err := os.Remove(steps + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		vmafStats, bandingStats, err := adapter.ComputeVMAF(ctx, gradient, steps, stream, tc.measures, nil, nil, nil)
		if errors.Is(err, ffmpeg.ErrVMAFModelUnavailable) {
			t.Skipf("the libvmaf of %s does not know %s: %s", ffmpeg.FFMPEGBinary, adapter.VMAFModel, err)
		}
		if err != nil {
			t.Fatalf("%s %+v: %s", tc.score, tc.measures, err)
		}
		if got := round(vmafStats.HarmonicMean); got != tc.vmaf {
			t.Errorf("%s %+v: expected a score of %v, got %v", tc.score, tc.measures, tc.vmaf, got)
		}
		if got := (core.BandingStats{AddedMean: round(bandingStats.AddedMean), AddedMax: round(bandingStats.AddedMax),
			SourceMean: round(bandingStats.SourceMean), EncodeMean: round(bandingStats.EncodeMean)}); got != tc.banding {
			t.Errorf("%s %+v: expected banding %+v, got %+v", tc.score, tc.measures, tc.banding, got)
		}
		if _, err := os.Stat(steps + tc.report); err != nil {
			t.Errorf("%s %+v: expected the report %s: %s", tc.score, tc.measures, tc.report, err)
		}
		if _, err := os.Stat(steps + tc.other); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s %+v: expected no report %s, got %v", tc.score, tc.measures, tc.other, err)
		}
	}
	// the score to gate must be named
	if _, _, err := (&EncoderAdapter{VMAFModel: ffmpeg.VMAFModelFHD}).ComputeVMAF(ctx, gradient, steps, stream,
		core.VMAFMeasures{Score: true}, nil, nil, nil); err == nil {
		t.Error("an adapter without a score to gate should refuse a score pass")
	}
}

// TestEncoderAdapter_EncodeVideoAlone encodes for real, with every encoder that works on the
// machine, a segment as a user's own split holds them: with audio, its video starting 60 ms
// after it. The encode must hold the video alone, every frame of it, and start with it: that is
// what puts the encodes of a pre-split directory on one frame grid whatever their segments
// hold (see ffmpeg.HEVCLibx265EncodeQP). An encoder that can not encode the video alone here
// (a GPU or a library the machine does not have) is skipped.
func TestEncoderAdapter_EncodeVideoAlone(t *testing.T) {
	for _, bin := range []string{ffmpeg.FFMPEGBinary, ffmpeg.FFProbeBinary} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found: %s", bin, err)
		}
	}
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, ffmpeg.FFMPEGBinary, append([]string{"-loglevel", "error", "-nostdin", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %s\n%s", args, err, out)
		}
	}
	const frames = 12
	videoOnly := filepath.Join(dir, "video.mkv")
	run("-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24000/1001", "-frames:v", "12", "-pix_fmt", "yuv420p10le", "-c:v", "ffv1", videoOnly)
	audio := filepath.Join(dir, "audio.mkv")
	run("-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "0.56", "-c:a", "aac", audio)
	segment := filepath.Join(dir, "segment.mkv")
	run("-itsoffset", "0.06", "-i", videoOnly, "-i", audio, "-map", "0:v", "-map", "1:a", "-c", "copy", segment)
	segmentStats, err := ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{Path: segment})
	if err != nil {
		t.Fatalf("probe: %s", err)
	}
	if gap, known := segmentStats.VideoStartInFile(); !known || gap != 60*time.Millisecond || len(segmentStats.Streams) != 2 {
		t.Fatalf("the segment was expected with 2 streams and its video 60 ms after its start, got %d streams and %s (known: %t)",
			len(segmentStats.Streams), gap, known)
	}
	for _, encoder := range []ffmpeg.Encoder{
		ffmpeg.HEVCEncoderLibx265, ffmpeg.HEVCEncoderNVEnc, ffmpeg.HEVCEncoderVAAPI, ffmpeg.HEVCEncoderD3D12VA, ffmpeg.HEVCEncoderVideoToolbox,
		ffmpeg.AV1EncoderLibaom, ffmpeg.AV1EncoderSVTAV1, ffmpeg.AV1EncoderNVEnc, ffmpeg.AV1EncoderVAAPI,
	} {
		t.Run(string(encoder), func(t *testing.T) {
			adapter := &EncoderAdapter{Encoder: encoder, VAAPIRendererPath: ffmpeg.VAAPIDefaultDevice}
			qpMin, qpMax, found := adapter.QPRange()
			if !found {
				t.Fatalf("no QP range for %s", encoder)
			}
			qp := (qpMin + qpMax) / 2
			encode := func(input, name string) (string, error) {
				stream, err := adapter.ProbeStream(ctx, input, nil, nil)
				if err != nil {
					t.Fatalf("probe %s: %s", input, err)
				}
				output := filepath.Join(t.TempDir(), name)
				return output, adapter.Encode(ctx, input, output, qp, stream, nil, nil, nil)
			}
			if _, err := encode(videoOnly, "video_only.mkv"); err != nil {
				t.Skipf("%s does not encode on this machine: %s", encoder, err)
			}
			output, err := encode(segment, "segment.mkv")
			if err != nil {
				t.Fatalf("encode of the segment with audio: %s", err)
			}
			stats, err := ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{Path: output, CountPackets: true})
			if err != nil {
				t.Fatalf("probe of the encode: %s", err)
			}
			if len(stats.Streams) != 1 || stats.VideoTrack() == nil {
				t.Fatalf("%d streams in the encode, want its video alone", len(stats.Streams))
			}
			if got := stats.VideoTrack().NbReadPackets; got != frames {
				t.Errorf("%d frames in the encode, want %d", got, frames)
			}
			if gap, known := stats.VideoStartInFile(); !known || gap != 0 {
				t.Errorf("the video of the encode starts %s after its file (known: %t), want with it", gap, known)
			}
		})
	}
}
