package ffmpeg

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenerateConcatList(t *testing.T) {
	tmpDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get the current directory: %v", err)
	}
	absFile := filepath.Join(tmpDir, `we'ird`, "seg_000001.mkv")

	listPath, err := GenerateConcatList(tmpDir, []string{
		filepath.Join("relative", "seg_000000.mkv"),
		absFile,
	}, nil)
	if err != nil {
		t.Fatalf("GenerateConcatList failed: %v", err)
	}
	content, err := os.ReadFile(string(listPath))
	if err != nil {
		t.Fatalf("failed to read the generated list: %v", err)
	}

	expected := concatScriptHeader + "\n" +
		// relative paths must be made absolute: ffmpeg resolves them against the list directory
		"file '" + filepath.Join(cwd, "relative", "seg_000000.mkv") + "'\n" +
		// quotes must be escaped the ffmpeg way
		"file '" + strings.ReplaceAll(absFile, `'`, `'\''`) + "'\n"
	if string(content) != expected {
		t.Errorf("unexpected concat list:\n%s\nexpected:\n%s", content, expected)
	}
}

func TestGenerateConcatList_NoFiles(t *testing.T) {
	if _, err := GenerateConcatList(t.TempDir(), nil, nil); err == nil {
		t.Error("expected an error with no files")
	}
}

func TestGenerateConcatList_Durations(t *testing.T) {
	tmpDir := t.TempDir()
	files := []string{
		filepath.Join(tmpDir, "seg_000000.mkv"),
		filepath.Join(tmpDir, "seg_000001.mkv"),
	}
	// 60 and 886 frames at 24000/1001 fps, rounded to the microsecond
	listPath, err := GenerateConcatList(tmpDir, files, []time.Duration{
		2502500 * time.Microsecond,
		36953583 * time.Microsecond,
	})
	if err != nil {
		t.Fatalf("GenerateConcatList failed: %v", err)
	}
	content, err := os.ReadFile(string(listPath))
	if err != nil {
		t.Fatalf("failed to read the generated list: %v", err)
	}
	expected := concatScriptHeader + "\n" +
		"file '" + files[0] + "'\n" +
		"duration 2.502500\n" +
		"file '" + files[1] + "'\n" +
		"duration 36.953583\n"
	if string(content) != expected {
		t.Errorf("unexpected concat list:\n%s\nexpected:\n%s", content, expected)
	}
}

func TestGenerateConcatList_DurationsMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	files := []string{filepath.Join(tmpDir, "a.mkv"), filepath.Join(tmpDir, "b.mkv")}
	if _, err := GenerateConcatList(tmpDir, files, []time.Duration{time.Second}); err == nil {
		t.Error("expected an error with fewer durations than files")
	}
	if _, err := GenerateConcatList(tmpDir, files, []time.Duration{time.Second, -time.Second}); err == nil {
		t.Error("expected an error with a negative duration")
	}
}

func TestFrameGridSnap(t *testing.T) {
	tests := []struct {
		frameRate string
		expected  string
	}{
		{"24000/1001", "setts=pts='if(eq(PTS,NOPTS),NOPTS,round(round(PTS*TB*24000/1001)*1001/TB/24000))'" +
			":dts='if(eq(DTS,NOPTS),NOPTS,round(round(DTS*TB*24000/1001)*1001/TB/24000))'"},
		{"25", "setts=pts='if(eq(PTS,NOPTS),NOPTS,round(round(PTS*TB*25/1)*1/TB/25))'" +
			":dts='if(eq(DTS,NOPTS),NOPTS,round(round(DTS*TB*25/1)*1/TB/25))'"},
		// 59.94 fps as the Matroska demuxer reads it
		{"19001/317", "setts=pts='if(eq(PTS,NOPTS),NOPTS,round(round(PTS*TB*19001/317)*317/TB/19001))'" +
			":dts='if(eq(DTS,NOPTS),NOPTS,round(round(DTS*TB*19001/317)*317/TB/19001))'"},
	}
	for _, tt := range tests {
		bsf, err := frameGridSnap(tt.frameRate)
		if err != nil {
			t.Errorf("frameGridSnap(%q) failed: %v", tt.frameRate, err)
			continue
		}
		if bsf != tt.expected {
			t.Errorf("frameGridSnap(%q) = %s\nexpected %s", tt.frameRate, bsf, tt.expected)
		}
	}
}

func TestFrameGridSnap_InvalidFrameRate(t *testing.T) {
	for _, frameRate := range []string{"", "23.976", "0/1", "24000/0", "-24000/1001", "24000/-1001", "24000/1001/1", "abc"} {
		if _, err := frameGridSnap(frameRate); err == nil {
			t.Errorf("expected an error with frame rate %q", frameRate)
		}
	}
}

// TestFrameGridSnap_Exact replays the expression written by frameGridSnap (pinned by
// TestFrameGridSnap) with the double precision operations of ffmpeg's evaluator, in the order
// it parses them (* and / from left to right), in the Matroska time base. Every timestamp up
// to 1.5 ms away from the exact time of its frame (two roundings move it by 1 ms at most) must
// land on that time rounded once, halves away from zero: at the start of a file, negative
// decode timestamps included, and after 1, 10, 24 and 100 hours.
func TestFrameGridSnap_Exact(t *testing.T) {
	const tb = 1.0 / 1000 // TB, av_q2d(1/1000)
	for _, rate := range []struct{ num, den int64 }{
		{24000, 1001}, {30000, 1001}, {60000, 1001}, {120000, 1001},
		{19001, 317}, // 59.94 fps as read from Matroska
		{24, 1}, {25, 1}, {30, 1}, {50, 1}, {60, 1}, {120, 1},
	} {
		num, den := float64(rate.num), float64(rate.den)
		for _, hours := range []int64{0, 1, 10, 24, 100} {
			first := hours*3600*rate.num/rate.den - 3
			for frame := first; frame < first+3000; frame++ {
				// the exact time of the frame is exact/rate.num ms
				exact := frame * rate.den * 1000
				expected := divRoundHalfAway(exact, rate.num)
				for timestamp := expected - 2; timestamp <= expected+2; timestamp++ {
					if 2*abs64(timestamp*rate.num-exact) > 3*rate.num {
						continue // more than 1.5 ms away
					}
					index := math.Round(float64(timestamp) * tb * num / den)
					if got := int64(math.Round(index * den / tb / num)); got != expected {
						t.Fatalf("%d/%d fps, frame %d: %d ms snapped to %d ms, expected %d ms",
							rate.num, rate.den, frame, timestamp, got, expected)
					}
				}
			}
		}
	}
}

// divRoundHalfAway returns a/b rounded to the nearest integer, halves away from zero (b > 0).
func divRoundHalfAway(a, b int64) int64 {
	if a < 0 {
		return -((-2*a + b) / (2 * b))
	}
	return (2*a + b) / (2 * b)
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
