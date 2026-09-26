package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestFFV1VideoMasterSegments checks that the master written cut into its segments gives the
// segments Segment cuts from the master file: the same packets, so the same pixels, with the
// same timestamps. The cuts are irregular, and the frame rates and containers are the ones where
// the two paths could round the timestamps differently: the master file counts in Matroska's
// milliseconds, the encoder in the source's time base. A single scene is the case where the
// segment muxer is not used.
func TestFFV1VideoMasterSegments(t *testing.T) {
	for _, bin := range []string{FFMPEGBinary, FFProbeBinary} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found: %s", bin, err)
		}
	}
	for _, tc := range []struct {
		rate, container string
		scenesFrames    []int
	}{
		{"24000/1001", "mkv", []int{37, 61, 90}},
		{"60000/1001", "mp4", []int{37, 61, 90}},
		{"30000/1001", "ts", []int{1, 50, 119}},
		{"25", "mp4", []int{60}},
		{"24000/1001", "mkv", nil},
	} {
		t.Run(fmt.Sprintf("%s %s %d cuts", tc.rate, tc.container, len(tc.scenesFrames)), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			// 120 frames with B-frames, their decoding order differs from their presentation one
			source := filepath.Join(dir, "source."+tc.container)
			if output, err := exec.CommandContext(ctx, FFMPEGBinary, "-loglevel", "error", "-f", "lavfi",
				"-i", "testsrc2=size=320x180:rate="+tc.rate, "-frames:v", "120", "-c:v", "mpeg4", "-bf", "2",
				"-q:v", "5", source).CombinedOutput(); err != nil {
				t.Fatalf("failed to create the source: %s\n%s", err, output)
			}
			// The master file, then cut
			master := filepath.Join(dir, "master.mkv")
			if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: source, OutputFilePath: master}); err != nil {
				t.Fatalf("master: %s", err)
			}
			cut := filepath.Join(dir, "cut")
			direct := filepath.Join(dir, "direct")
			for _, d := range []string{cut, direct} {
				if err := os.Mkdir(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := Segment(ctx, SegmentConfig{Input: master, ScenesFrames: tc.scenesFrames, OutputDir: cut}); err != nil {
				t.Fatalf("segment: %s", err)
			}
			// The master written cut
			if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: source, SegmentsDir: direct,
				ScenesFrames: tc.scenesFrames}); err != nil {
				t.Fatalf("master cut into its segments: %s", err)
			}
			// Same segments, packet by packet
			for i := range len(tc.scenesFrames) + 1 {
				name := fmt.Sprintf(SegmentOutputFormat, i)
				want := probePackets(t, filepath.Join(cut, name))
				got := probePackets(t, filepath.Join(direct, name))
				if !slices.Equal(got, want) {
					t.Errorf("segment %s differs:\n got %d packets, first %v\nwant %d packets, first %v",
						name, len(got), got[:min(3, len(got))], len(want), want[:min(3, len(want))])
				}
			}
			if entries, _ := os.ReadDir(direct); len(entries) != len(tc.scenesFrames)+1 {
				t.Errorf("got %d segments, want %d", len(entries), len(tc.scenesFrames)+1)
			}
		})
	}
}

// probePackets returns the timestamp, duration, flags and data hash of every packet of a file.
func probePackets(t *testing.T, path string) []string {
	t.Helper()
	output, err := exec.Command(FFProbeBinary, "-v", "error", "-show_data_hash", "md5",
		"-show_entries", "packet=pts,duration,flags,data_hash", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %s", path, err)
	}
	return strings.Split(strings.TrimSpace(string(output)), "\n")
}
