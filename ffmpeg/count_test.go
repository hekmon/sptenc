package ffmpeg

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// Real (shortened) ffmpeg 9.0.1 logs of a CountFrames run on a 24 fps Matroska file: the usual
// chatter at the info level, then two lines per frame printed by the metadata filter. Times are
// rounded to the millisecond by the container: 41 or 42 ms apart. One frame has no timestamp,
// one line is an error to report.
const countFramesSampleLogs = `[info] ffmpeg stats and -progress period set to 0.1.
[info] Input #0, matroska,webm, from 'clip.mkv':
[info]   Duration: 00:00:02.02, start: 0.000000, bitrate: 177 kb/s
[info]   Stream #0:0: Video: h264 (High), yuv420p(progressive), 160x90 [SAR 1:1 DAR 16:9], 23.98 fps, 23.98 tbr, 1k tbn (default)
[info] Stream mapping:
[info]   Stream #0:0 -> #0:0 (h264 (native) -> wrapped_avframe (native))
[info] Output #0, null, to 'pipe:':
[Parsed_metadata_2 @ 0x79954061c0] [info] frame:0    pts:0       pts_time:0
[Parsed_metadata_2 @ 0x79954061c0] [info] sptenc=1
[Parsed_metadata_2 @ 0x79954061c0] [info] frame:1    pts:42000   pts_time:0.042
[Parsed_metadata_2 @ 0x79954061c0] [info] sptenc=1
[Parsed_metadata_2 @ 0x79954061c0] [info] frame:2    pts:83000   pts_time:0.083
[Parsed_metadata_2 @ 0x79954061c0] [info] sptenc=1
[h264 @ 0x7995406000] [error] concealing 6 DC, 6 AC, 6 MV errors in P frame
[Parsed_metadata_2 @ 0x79954061c0] [info] frame:3    pts:125000  pts_time:0.125
[Parsed_metadata_2 @ 0x79954061c0] [info] sptenc=1
[Parsed_metadata_2 @ 0x79954061c0] [info] frame:4    pts:NOPTS   pts_time:NOPTS
[Parsed_metadata_2 @ 0x79954061c0] [info] sptenc=1
[Parsed_metadata_2 @ 0x79954061c0] [info] frame:5    pts:208000  pts_time:0.208
[Parsed_metadata_2 @ 0x79954061c0] [info] sptenc=1
[Parsed_metadata_2 @ 0x79954061c0] [info] frame:6    pts:250000  pts_time:0.25
[Parsed_metadata_2 @ 0x79954061c0] [info] sptenc=1
[out#0/null @ 0x7995404300] [info] video:20KiB audio:0KiB subtitle:0KiB other streams:0KiB global headers:0KiB muxing overhead: unknown
[info] frame=    7 fps=0.0 q=-0.0 Lsize=N/A time=00:00:00.25 bitrate=N/A speed= 433x elapsed=0:00:00.00
`

func TestCountFramesLogs(t *testing.T) {
	var reported []error
	nbDurations, shortest, longest := countFramesLogs(io.NopCloser(strings.NewReader(countFramesSampleLogs)), func(err error) {
		reported = append(reported, err)
	})
	// Frame durations: 42, 41, 42 then 42 ms. The frame without a time breaks the chain: the
	// 83 ms between the frames around it are not a duration.
	if nbDurations != 4 || shortest != 41*time.Millisecond || longest != 42*time.Millisecond {
		t.Errorf("unexpected frame durations: %d measured, from %v to %v", nbDurations, shortest, longest)
	}
	stream := &FFProbeBinaryStream{RFrameRate: "24000/1001", AvgFrameRate: "24000/1001"}
	stream.SetReadFrames(ReadFrames{Nb: 7, NbDurations: nbDurations, ShortestDuration: shortest, LongestDuration: longest})
	if stream.NbReadFrames != 7 || !stream.IsConstantFrameRate() {
		t.Errorf("a 24 fps Matroska stream must be seen as a constant frame rate one: %+v", stream)
	}
	// Only the error line is reported, the info chatter is not
	if len(reported) != 1 || !strings.Contains(reported[0].Error(), "concealing 6 DC") {
		t.Errorf("expected the error line to be reported alone, got %v", reported)
	}
	// No callback must not be an issue
	if nb, _, _ := countFramesLogs(io.NopCloser(strings.NewReader(countFramesSampleLogs)), nil); nb != 4 {
		t.Errorf("expected 4 durations without callback, got %d", nb)
	}
}

func TestParseMetadataPTS(t *testing.T) {
	for name, tc := range map[string]struct {
		line     string
		expected time.Duration
		known    bool
		fails    bool
	}{
		"microseconds": {"[Parsed_metadata_2 @ 0x79954061c0] [info] frame:47   pts:1960000 pts_time:1.96", 1960 * time.Millisecond, true, false},
		"zero":         {"[Parsed_metadata_2 @ 0x79954061c0] [info] frame:0    pts:0       pts_time:0", 0, true, false},
		"no timestamp": {"[Parsed_metadata_2 @ 0x79954061c0] [info] frame:4    pts:NOPTS   pts_time:NOPTS", 0, false, false},
		"no pts field": {"[Parsed_metadata_2 @ 0x79954061c0] [info] frame:4    pts_time:0.125", 0, false, true},
		"garbage":      {"[Parsed_metadata_2 @ 0x79954061c0] [info] frame:4    pts:abc pts_time:0.125", 0, false, true},
		"not a frame":  {"[Parsed_metadata_2 @ 0x79954061c0] [info] sptenc=1", 0, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			frameTime, known, err := parseMetadataPTS(tc.line)
			if tc.fails {
				if err == nil {
					t.Error("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if known != tc.known || frameTime != tc.expected {
				t.Errorf("expected %v (known %t), got %v (known %t)", tc.expected, tc.known, frameTime, known)
			}
		})
	}
}

// The last progress block (progress=end) carries the final state: it must be reported.
func TestStandardProgress_ReportsEnd(t *testing.T) {
	const output = "frame=10\nfps=0.00\nout_time_us=100000\nspeed=1x\nprogress=continue\n" +
		"frame=48\nfps=0.00\nout_time_us=2002000\nspeed=433x\nprogress=end\n"
	var frames []int
	standardProgress(io.NopCloser(strings.NewReader(output)), func(stats ProgressStats) {
		frames = append(frames, stats.CurrentFrame)
	}, func(err error) {
		if !errors.Is(err, io.EOF) {
			t.Errorf("unexpected runtime error: %v", err)
		}
	})
	if len(frames) != 2 || frames[0] != 10 || frames[1] != 48 {
		t.Errorf("expected frames [10 48], got %v", frames)
	}
}
