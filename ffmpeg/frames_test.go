package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The size TransNetV2 reads its frames at.
const (
	feedFramesTestWidth  = 48
	feedFramesTestHeight = 27
	feedFramesTestSize   = feedFramesTestWidth * feedFramesTestHeight * 3
)

// feedFramesTestTools skips the test without ffmpeg and returns a function running it, failing the
// test when it fails.
func feedFramesTestTools(t *testing.T) func(args ...string) {
	t.Helper()
	if _, err := exec.LookPath(FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", FFMPEGBinary, err)
	}
	return func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(t.Context(), FFMPEGBinary, append([]string{"-loglevel", "error", "-nostdin", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %s\n%s", args, err, output)
		}
	}
}

// feedFramesWriter records what FeedFrames writes, checking every Write is one whole frame.
type feedFramesWriter struct {
	t      *testing.T
	frames bytes.Buffer
	writes int
}

func (w *feedFramesWriter) Write(p []byte) (int, error) {
	w.writes++
	if len(p) != feedFramesTestSize {
		w.t.Errorf("write %d: %d bytes instead of a frame of %d", w.writes, len(p), feedFramesTestSize)
	}
	return w.frames.Write(p)
}

// feedFramesBytes feeds the frames of path, failing the test on an error or a runtime error.
func feedFramesBytes(t *testing.T, path string, videoToolbox bool) (frames int, raw []byte) {
	t.Helper()
	w := &feedFramesWriter{t: t}
	frames, err := FeedFrames(t.Context(), FeedFramesConfig{
		Path: path, Width: feedFramesTestWidth, Height: feedFramesTestHeight, Output: w,
		VideoToolboxDec: videoToolbox,
		RuntimeError:    func(err error) { t.Errorf("%s: unexpected runtime error: %s", filepath.Base(path), err) },
	})
	if err != nil {
		t.Fatalf("%s: %s", filepath.Base(path), err)
	}
	if w.frames.Len() != frames*feedFramesTestSize {
		t.Errorf("%s: %d frames returned, %d bytes written", filepath.Base(path), frames, w.frames.Len())
	}
	return frames, w.frames.Bytes()
}

// TestFeedFramesCount feeds for real the frames of files whose timestamps do not fill the timeline
// at a constant rate: an MP4 whose video starts 0.5 s after its audio, and a Matroska file missing
// 12 frames in its middle. The frames written are those CountFrames counts (240 and 228), each of
// them one whole frame. Without -fps_mode passthrough, ffmpeg wrote 252 and 240 (see WHY
// -fps_mode passthrough in FeedFrames).
func TestFeedFramesCount(t *testing.T) {
	run := feedFramesTestTools(t)
	dir := t.TempDir()
	delayed := filepath.Join(dir, "delayed.mp4")
	run("-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24:duration=10", "-f", "lavfi", "-i", "sine=duration=10.5",
		"-filter_complex", "[0:v]setpts=PTS+0.5/TB[v]", "-map", "[v]", "-map", "1:a", "-c:v", "mpeg4", "-q:v", "5", "-c:a", "aac", delayed)
	hole := filepath.Join(dir, "hole.mkv")
	run("-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24:duration=10", "-vf", "select='not(between(n,100,111))'",
		"-fps_mode", "passthrough", "-c:v", "mpeg4", "-q:v", "5", hole)
	for _, tc := range []struct {
		path string
		want int
	}{{delayed, 240}, {hole, 228}} {
		counted, err := CountFrames(t.Context(), CountFramesConfig{Path: tc.path})
		if err != nil {
			t.Fatalf("%s: counting the frames: %s", filepath.Base(tc.path), err)
		}
		if counted.Nb != tc.want {
			t.Fatalf("%s: the clip was expected with %d frames, CountFrames counts %d", filepath.Base(tc.path), tc.want, counted.Nb)
		}
		if frames, _ := feedFramesBytes(t, tc.path, false); frames != counted.Nb {
			t.Errorf("%s: %d frames written, CountFrames counts %d", filepath.Base(tc.path), frames, counted.Nb)
		}
	}
}

// TestFeedFramesPixels feeds for real a clip of flat colours changing at known frames (red 0 to 6,
// green 7 to 13, blue 14 to 25, grey 26 to 37), converted from RGB to 4:2:0 limited range with
// BT.709's matrix and declared so: every pixel of frame N is the colour of frame N of the source,
// within 4 codes. Measured: 3 at most (255 comes back as 252 to 254, 128 as 126: the YUV codes are
// the exact BT.709 ones, the scaler's default conversion to RGB is that close). Relabelled without a
// colour description, as scene detection relabels them (see WHY NO RELABELLING in FeedFrames), the
// frames are converted with BT.601's matrix: red came out 230, green (18, 255, 8).
func TestFeedFramesPixels(t *testing.T) {
	run := feedFramesTestTools(t)
	dir := t.TempDir()
	colours := []struct {
		name    string
		frames  int
		r, g, b int
	}{{"0xFF0000", 7, 255, 0, 0}, {"0x00FF00", 7, 0, 255, 0}, {"0x0000FF", 12, 0, 0, 255}, {"0x808080", 12, 128, 128, 128}}
	var inputs []string
	var filter strings.Builder
	for i, colour := range colours {
		inputs = append(inputs, "-f", "lavfi", "-i", "color=c="+colour.name+":size=320x240:rate=24:duration=1")
		filter.WriteString("[" + strconv.Itoa(i) + ":v]format=rgb24,trim=end_frame=" + strconv.Itoa(colour.frames) + ",setpts=PTS-STARTPTS[c" + strconv.Itoa(i) + "];")
	}
	for i := range colours {
		filter.WriteString("[c" + strconv.Itoa(i) + "]")
	}
	filter.WriteString("concat=n=" + strconv.Itoa(len(colours)) + ":v=1:a=0,scale=out_color_matrix=bt709:out_range=tv,format=yuv420p[v]")
	clip := filepath.Join(dir, "colours.mkv")
	run(append(inputs, "-filter_complex", filter.String(), "-map", "[v]", "-c:v", "ffv1",
		"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv", clip)...)
	frames, raw := feedFramesBytes(t, clip, false)
	if frames != 38 {
		t.Fatalf("%d frames written instead of 38", frames)
	}
	const tolerance = 4
	frame := 0
	for _, colour := range colours {
		for range colour.frames {
			worst := 0
			pixels := raw[frame*feedFramesTestSize : (frame+1)*feedFramesTestSize]
			for p := 0; p < len(pixels); p += 3 {
				for c, want := range []int{colour.r, colour.g, colour.b} {
					worst = max(worst, feedFramesAbs(int(pixels[p+c])-want))
				}
			}
			if worst > tolerance {
				t.Errorf("frame %d: expected %s within %d codes, a component is %d codes off (first pixel %v)",
					frame, colour.name, tolerance, worst, pixels[:3])
			}
			frame++
		}
	}
}

func feedFramesAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// TestFeedFramesPropertiesChange feeds for real H.264 clips whose second part (frames 14 to 37)
// declares other properties than the first: a colour description appearing is accepted, the bytes
// those of each part fed on its own; a range, a matrix, a picture size and a pixel format changing
// fail with pictureChange's errors, as scene detection does with scdet. Skipped without libx264.
func TestFeedFramesPropertiesChange(t *testing.T) {
	run := feedFramesTestTools(t)
	dir := t.TempDir()
	x264 := []string{"-c:v", "libx264", "-qp", "10", "-g", "12", "-bf", "0"}
	twoParts := func(name, firstBSF, secondBSF, secondFilter string) (path string, parts []string) {
		for i, part := range []struct{ source, filter, bsf string }{
			{"testsrc2=size=320x240:rate=24", "format=yuv420p,setsar=1", firstBSF},
			{"testsrc=size=320x240:rate=24", secondFilter, secondBSF},
		} {
			parts = append(parts, filepath.Join(dir, name+"_"+strconv.Itoa(i)+".ts"))
			args := append([]string{"-f", "lavfi", "-i", part.source, "-frames:v", strconv.Itoa(14 + 10*i), "-vf", part.filter}, x264...)
			if part.bsf != "" {
				args = append(args, "-bsf:v", part.bsf)
			}
			if output, err := exec.CommandContext(t.Context(), FFMPEGBinary, append(append([]string{"-loglevel", "error", "-nostdin", "-y"}, args...), parts[i])...).CombinedOutput(); err != nil {
				t.Skipf("can not make the H.264 clips (libx264 needed): %s\n%s", err, output)
			}
		}
		path = filepath.Join(dir, name+".ts")
		run("-i", "concat:"+strings.Join(parts, "|"), "-map", "0:v:0", "-c", "copy", path)
		return
	}
	const (
		h264BT601 = "h264_metadata=colour_primaries=6:transfer_characteristics=6:matrix_coefficients=6"
		h264BT709 = "h264_metadata=colour_primaries=1:transfer_characteristics=1:matrix_coefficients=1"
		sameKind  = "format=yuv420p,setsar=1"
	)
	// accepted, and fed as each part is
	source, parts := twoParts("colour_description", "", h264BT709, sameKind)
	frames, raw := feedFramesBytes(t, source, false)
	var each []byte
	for _, part := range parts {
		_, partRaw := feedFramesBytes(t, part, false)
		each = append(each, partRaw...)
	}
	if frames != 38 || !bytes.Equal(raw, each) {
		t.Errorf("colour description appearing: %d frames, the same bytes as each part fed on its own: %t", frames, bytes.Equal(raw, each))
	}
	// refused
	for _, tc := range []struct{ name, firstBSF, secondBSF, secondFilter, want string }{
		{"range", "", "h264_metadata=video_full_range_flag=1", sameKind, "changes its range (tv to pc) mid-stream"},
		{"matrix", h264BT601, h264BT709, sameKind, "changes its matrix (smpte170m to bt709) mid-stream"},
		{"size", "", "", "format=yuv420p,scale=352:288,setsar=1", "changes its picture size or pixel format mid-stream (320x240 to 352x288)"},
		{"pixel format", "", "", "format=yuv422p,setsar=1", "changes its picture size or pixel format mid-stream"},
	} {
		source, _ := twoParts(strings.ReplaceAll(tc.name, " ", "_"), tc.firstBSF, tc.secondBSF, tc.secondFilter)
		frames, err := FeedFrames(t.Context(), FeedFramesConfig{Path: source, Width: feedFramesTestWidth, Height: feedFramesTestHeight, Output: io.Discard})
		if err == nil {
			t.Errorf("%s change: expected an error, got %d frames", tc.name, frames)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s change: expected %q in the error, got %s", tc.name, tc.want, err)
		}
	}
}

// feedFramesLongClip makes a clip of 2400 frames, far more than the pipes between ffmpeg and
// FeedFrames hold: an ffmpeg left behind with nobody reading would block.
func feedFramesLongClip(t *testing.T, run func(args ...string)) string {
	t.Helper()
	clip := filepath.Join(t.TempDir(), "long-"+strconv.FormatInt(time.Now().UnixNano(), 36)+".mkv")
	run("-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24:duration=100", "-c:v", "mpeg4", "-q:v", "10", clip)
	return clip
}

// feedFramesPID returns the PID of the ffmpeg reading path, 0 when there is none, through pgrep
// and ps (the test is skipped without them).
func feedFramesPID(t *testing.T, path string) int {
	t.Helper()
	output, err := exec.Command("pgrep", "-f", path).Output()
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.Fields(string(output))[0])
	return pid
}

// feedFramesStoppingWriter calls stop on its write number at (from 1) and returns what it returns.
type feedFramesStoppingWriter struct {
	at, writes int
	stop       func() error
}

func (w *feedFramesStoppingWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.at {
		if err := w.stop(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// TestFeedFramesStop stops for real the feed of a long clip, the writer failing on its fifth frame,
// the context cancelled on the third one, and the context cancelled while the writer stalls (the
// reader of a helper which stopped reading): each call returns promptly with its cause (the
// writer's error, context.Canceled), nothing is written after it, the ffmpeg process is gone
// (reaped: ps does not know it anymore, not even as a zombie) and no goroutine is left. Skipped
// without pgrep and ps.
func TestFeedFramesStop(t *testing.T) {
	run := feedFramesTestTools(t)
	for _, bin := range []string{"pgrep", "ps"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found: %s", bin, err)
		}
	}
	errWriter := errors.New("the helper is gone")
	for _, tc := range []struct {
		name        string
		at          int
		stop        func(ctx context.Context, cancel context.CancelFunc) error
		wantErr     error
		wantWritten int
	}{
		{"writer error", 5, func(context.Context, context.CancelFunc) error { return errWriter }, errWriter, 4},
		{"context cancelled", 3, func(_ context.Context, cancel context.CancelFunc) error { cancel(); return nil }, context.Canceled, 3},
		{"context cancelled while the writer stalls", 3, func(ctx context.Context, cancel context.CancelFunc) error {
			time.AfterFunc(300*time.Millisecond, cancel)
			<-ctx.Done()
			return nil
		}, context.Canceled, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clip := feedFramesLongClip(t, run)
			goroutines := runtime.NumGoroutine()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var pid int
			w := &feedFramesStoppingWriter{at: tc.at}
			w.stop = func() error {
				pid = feedFramesPID(t, clip)
				return tc.stop(ctx, cancel)
			}
			type result struct {
				frames int
				err    error
			}
			done := make(chan result, 1)
			start := time.Now()
			go func() {
				frames, err := FeedFrames(ctx, FeedFramesConfig{Path: clip, Width: feedFramesTestWidth, Height: feedFramesTestHeight, Output: w,
					RuntimeError: func(err error) { t.Errorf("unexpected runtime error: %s", err) }})
				done <- result{frames, err}
			}()
			var res result
			select {
			case res = <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("FeedFrames did not return within 20 s")
			}
			elapsed := time.Since(start)
			if !errors.Is(res.err, tc.wantErr) {
				t.Errorf("expected the error to be %v, got %v", tc.wantErr, res.err)
			}
			if res.frames != tc.wantWritten || w.writes != tc.at {
				t.Errorf("expected %d frames written and %d writes, got %d and %d", tc.wantWritten, tc.at, res.frames, w.writes)
			}
			if elapsed > 3*time.Second {
				t.Errorf("FeedFrames took %v to return", elapsed)
			}
			if pid == 0 {
				t.Fatal("no ffmpeg process found while it was feeding the frames")
			}
			if output, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output(); err == nil {
				t.Errorf("the ffmpeg process %d is still there (state %s)", pid, strings.TrimSpace(string(output)))
			}
			for deadline := time.Now().Add(time.Second); runtime.NumGoroutine() > goroutines && time.Now().Before(deadline); {
				time.Sleep(10 * time.Millisecond)
			}
			if n := runtime.NumGoroutine(); n > goroutines {
				t.Errorf("%d goroutines left after FeedFrames, %d before", n, goroutines)
			}
		})
	}
}

// TestFeedFramesReport feeds for real a long clip: the frames reported only go up, end at the
// number of frames returned, and come at most once per StatsPeriod (plus the final one).
func TestFeedFramesReport(t *testing.T) {
	run := feedFramesTestTools(t)
	clip := feedFramesLongClip(t, run)
	var reports []int
	start := time.Now()
	frames, err := FeedFrames(t.Context(), FeedFramesConfig{Path: clip, Width: feedFramesTestWidth, Height: feedFramesTestHeight, Output: io.Discard,
		FramesReport: func(frames int) { reports = append(reports, frames) }})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if frames != 2400 {
		t.Errorf("%d frames written instead of 2400", frames)
	}
	if len(reports) == 0 || reports[len(reports)-1] != frames {
		t.Fatalf("the reports %v do not end at %d", reports, frames)
	}
	for i := 1; i < len(reports); i++ {
		if reports[i] <= reports[i-1] {
			t.Errorf("report %d goes from %d to %d", i, reports[i-1], reports[i])
		}
	}
	if limit := int(elapsed/StatsPeriod) + 2; len(reports) > limit {
		t.Errorf("%d reports in %v, more than one per %v (%d at most)", len(reports), elapsed, StatsPeriod, limit)
	}
}

// TestFeedFramesVideoToolbox feeds for real an 8-bit H.264 clip decoded by VideoToolbox: the bytes
// are those of a software decode. Skipped without VideoToolbox (checked by decoding the clip to
// VideoToolbox surfaces, which does not fall back to software) or libx264.
func TestFeedFramesVideoToolbox(t *testing.T) {
	feedFramesTestTools(t)
	if runtime.GOOS != "darwin" {
		t.Skip("VideoToolbox is macOS only")
	}
	clip := filepath.Join(t.TempDir(), "h264.mkv")
	if output, err := exec.CommandContext(t.Context(), FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=2", "-vf", "format=yuv420p",
		"-c:v", "libx264", "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", clip).CombinedOutput(); err != nil {
		t.Skipf("can not make the H.264 clip (libx264 needed): %s\n%s", err, output)
	}
	if output, err := exec.CommandContext(t.Context(), FFMPEGBinary, "-loglevel", "error", "-nostdin",
		"-hwaccel", "videotoolbox", "-hwaccel_output_format", "videotoolbox_vld", "-i", clip, "-f", "null", "-").CombinedOutput(); err != nil {
		t.Skipf("VideoToolbox does not decode the clip here: %s\n%s", err, output)
	}
	softwareFrames, software := feedFramesBytes(t, clip, false)
	hardwareFrames, hardware := feedFramesBytes(t, clip, true)
	if softwareFrames != 48 || hardwareFrames != softwareFrames || !bytes.Equal(hardware, software) {
		t.Errorf("decoded by VideoToolbox, %d frames, the bytes of the software decode (%d frames): %t",
			hardwareFrames, softwareFrames, bytes.Equal(hardware, software))
	}
}

// Lines of ffmpeg n9.0.1 at -loglevel level+warning: the changes pictureChange refuses fail the
// feed, the other warnings go to debug, the errors and the lines without a level to runtimeError.
func TestFeedFramesLogs(t *testing.T) {
	const (
		accepted = "[graph -1 input from stream 0:0 @ 0x7a97025bc0] [warning] filter context - w: 320 h: 240 fmt: 0 csp: unknown range: unknown alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 0 csp: bt709 range: tv alpha: unspecified pts_time: 0.583333"
		refused  = "[graph -1 input from stream 0:0 @ 0x7763031bc0] [warning] filter context - w: 320 h: 240 fmt: 0 csp: unknown range: unknown alpha: unspecified, incoming frame - w: 352 h: 288 fmt: 0 csp: unknown range: unknown alpha: unspecified pts_time: 0.583333"
		onTheFly = "[graph -1 input from stream 0:0 @ 0x7a97025bc0] [warning] Changing video frame properties on the fly is not supported by all filters."
		scaler   = "[swscaler @ 0x75f2ad4000] [warning] deprecated pixel format used, make sure you did set range correctly"
		decoder  = "[mpeg2video @ 0x74d8c51500] [error] Failed setup for format videotoolbox_vld: hwaccel initialisation returned error."
		noCtx    = "[error] Unrecognized option 'hwaccel videotoolbox'."
		noLevel  = "some library writing to stderr"
	)
	for name, tc := range map[string]struct {
		lines            []string
		wantErr          bool
		debugs, runtimes int
	}{
		"change accepted": {[]string{onTheFly, accepted}, false, 1, 0},
		"change refused":  {[]string{onTheFly, refused, accepted}, true, 1, 0},
		"warnings":        {[]string{scaler}, false, 1, 0},
		"errors":          {[]string{decoder, noCtx, noLevel}, false, 0, 3},
	} {
		t.Run(name, func(t *testing.T) {
			var debugs, runtimes int
			err := feedFramesLogs(io.NopCloser(strings.NewReader(strings.Join(tc.lines, "\n")+"\n")),
				func(string) { debugs++ }, func(error) { runtimes++ })
			if (err != nil) != tc.wantErr {
				t.Errorf("expected an error: %t, got %v", tc.wantErr, err)
			}
			if debugs != tc.debugs || runtimes != tc.runtimes {
				t.Errorf("expected %d debug and %d runtime error lines, got %d and %d", tc.debugs, tc.runtimes, debugs, runtimes)
			}
		})
	}
}
