package transnetv2

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary is its own fake helper: run with fakeModeEnv set, TestMain runs fakeHelper
// instead of the tests, in the mode it names.
const (
	fakeModeEnv    = "SPTENC_TRANSNETV2_FAKE"
	fakePIDFileEnv = "SPTENC_TRANSNETV2_FAKE_PIDFILE" // where the fake writes its PID
	fakeWindow     = 100                              // the fake answers 100 frames late, as the model may
	fakeSHA256     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeModeEnv); mode != "" {
		os.Exit(fakeHelper(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeProbability is the probability the fake gives a frame: derived from the frame index the
// test feed writes in its first two bytes, so that the order is checked.
func fakeProbability(index uint16) float32 {
	return float32(index%1000) / 1000
}

// fakeHelper behaves as the helper in the given mode and returns its exit status.
func fakeHelper(mode string, args []string) int {
	if path := os.Getenv(fakePIDFileEnv); path != "" {
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 125
		}
	}
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "fake: unexpected arguments %q\n", args)
		return 126
	}
	switch args[0] {
	case "version":
		line := fmt.Sprintf(`{"protocol":1,"detector":"transnetv2","version":"0.1.0","model":"transnetv2.onnx","model_sha256":"%s"}`, fakeSHA256)
		switch mode {
		case "protocol2":
			line = strings.Replace(line, `"protocol":1`, `"protocol":2`, 1)
		case "otherdetector":
			line = strings.Replace(line, `"detector":"transnetv2"`, `"detector":"autoshot"`, 1)
		case "garbage":
			line = "TransNetV2 helper 0.1.0"
		case "versionfail":
			fmt.Fprintln(os.Stderr, "fake: no model file")
			return 2
		}
		fmt.Println(line)
		return 0
	case "detect":
		return fakeDetect(mode)
	default:
		fmt.Fprintf(os.Stderr, "fake: unknown command %q\n", args[0])
		return 126
	}
}

func fakeDetect(mode string) int {
	fmt.Fprintln(os.Stderr, "fake: model loaded")
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	var pending []string
	frame := make([]byte, FrameSize)
	read := 0
	for {
		if _, err := io.ReadFull(in, frame); err != nil {
			break
		}
		read++
		switch {
		case read == 5 && mode == "earlyexit":
			out.Flush()
			fmt.Fprintln(os.Stderr, "fake: out of memory")
			return 3
		case read == 5 && mode == "exit0early":
			return 0
		case read == 2 && mode == "hang":
			out.Flush()
			time.Sleep(time.Hour)
		case read == 2 && mode == "closehang":
			out.Flush()
			os.Stdin.Close()
			time.Sleep(time.Hour)
		}
		line := strconv.FormatFloat(float64(fakeProbability(binary.BigEndian.Uint16(frame))), 'g', -1, 32)
		switch {
		case read == 4 && mode == "notnumber":
			line = "abc"
		case read == 4 && mode == "outofrange":
			line = "1.5"
		}
		if pending = append(pending, line); len(pending) > fakeWindow {
			fmt.Fprintln(out, pending[0])
			pending = pending[1:]
		}
	}
	switch mode {
	case "few":
		pending = pending[:len(pending)-1]
	case "many":
		pending = append(pending, "0.5")
	}
	for _, line := range pending {
		fmt.Fprintln(out, line)
	}
	if mode == "exitafter" {
		out.Flush()
		fmt.Fprintln(os.Stderr, "fake: cleanup failed")
		return 4
	}
	return 0
}

// useFake makes the test binary the helper, in the given mode, and returns the file it writes its
// PID to.
func useFake(t *testing.T, mode string) (pidFile string) {
	t.Helper()
	previous := Helper
	t.Cleanup(func() { Helper = previous })
	Helper = os.Args[0]
	pidFile = filepath.Join(t.TempDir(), "pid")
	t.Setenv(fakeModeEnv, mode)
	t.Setenv(fakePIDFileEnv, pidFile)
	// under -race, a Go program exiting with status 0 sleeps a second first
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	return
}

// testFeed writes n frames, frame i holding i in its first two bytes and its low byte in the
// others, then extra bytes, and reports n+lie frames. Unless ignoreCtx, it stops when ctx is done.
type testFeed struct {
	n, extra, lie int
	ignoreCtx     bool
	failAt        int    // return failErr before writing frame failAt, if non zero...
	failErr       error  // ...
	failAfter     string // ...once this file exists (the fake's PID file: the helper is running)
}

func (f testFeed) feed(ctx context.Context, w io.Writer) (int, error) {
	frame := make([]byte, FrameSize)
	for i := range f.n {
		if f.failAt != 0 && i == f.failAt {
			for f.failAfter != "" {
				if _, err := os.Stat(f.failAfter); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			return i, f.failErr
		}
		if err := ctx.Err(); err != nil && !f.ignoreCtx {
			return i, err
		}
		for j := range frame {
			frame[j] = byte(i)
		}
		binary.BigEndian.PutUint16(frame, uint16(i))
		if _, err := w.Write(frame); err != nil {
			// as ffmpeg.FeedFrames wraps the error of its writer
			return i, fmt.Errorf("error writing frame %d: %w", i, err)
		}
	}
	if f.extra > 0 {
		if _, err := w.Write(make([]byte, f.extra)); err != nil {
			return f.n, err
		}
	}
	return f.n + f.lie, nil
}

// detectAndCheck runs Detect in its own goroutine, failing the test if it does not return within
// 20 s, then checks that the helper was reaped (ps does not know its PID anymore, not even as a
// zombie; skipped without ps) and that no goroutine is left.
func detectAndCheck(t *testing.T, ctx context.Context, pidFile string, config DetectConfig) (probabilities []float32, err error, elapsed time.Duration) {
	t.Helper()
	goroutines := runtime.NumGoroutine()
	type result struct {
		probabilities []float32
		err           error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		p, err := Detect(ctx, config)
		done <- result{p, err}
	}()
	var res result
	select {
	case res = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Detect did not return within 20 s")
	}
	elapsed = time.Since(start)
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("the fake helper did not write its PID: %s", readErr)
	}
	if _, lookErr := exec.LookPath("ps"); lookErr == nil {
		if output, psErr := exec.Command("ps", "-o", "stat=", "-p", string(raw)).Output(); psErr == nil {
			t.Errorf("the helper process %s is still there (state %s)", raw, strings.TrimSpace(string(output)))
		}
	}
	for deadline := time.Now().Add(time.Second); runtime.NumGoroutine() > goroutines && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > goroutines {
		t.Errorf("%d goroutines left after Detect, %d before", n, goroutines)
	}
	return res.probabilities, res.err, elapsed
}

func TestGetVersion(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		wantErr string // empty: no error
	}{
		{"ok", ""},
		{"protocol2", "protocol 2 stated, this sptenc speaks protocol 1"},
		{"otherdetector", `detector "autoshot" stated, expected "transnetv2"`},
		{"garbage", `not a JSON version object: "TransNetV2 helper 0.1.0"`},
		{"versionfail", "exit status 2\nfake: no model file"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			useFake(t, tc.mode)
			info, err := GetVersion(t.Context())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				want := VersionInfo{Protocol: 1, Detector: "transnetv2", Version: "0.1.0", Model: "transnetv2.onnx", ModelSHA256: fakeSHA256}
				if info != want {
					t.Errorf("expected %+v, got %+v", want, info)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected an error containing %q, got %v", tc.wantErr, err)
			}
			if info != (VersionInfo{}) {
				t.Errorf("expected the zero value with an error, got %+v", info)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		previous := Helper
		defer func() { Helper = previous }()
		Helper = filepath.Join(t.TempDir(), "sptenc-detector-transnetv2")
		if _, err := GetVersion(t.Context()); err == nil || !strings.Contains(err.Error(), "error starting "+Helper) {
			t.Errorf("expected an error starting the helper, got %v", err)
		}
	})
}

func TestParseVersion(t *testing.T) {
	ok := `{"protocol":1,"detector":"transnetv2","version":"0.1.0","model":"m.onnx","model_sha256":"` + fakeSHA256 + `"}`
	for _, tc := range []struct {
		name, output, wantErr string
	}{
		{"ok", ok + "\n", ""},
		{"ok with CRLF", ok + "\r\n", ""},
		{"empty", "", "no version line"},
		{"two lines", ok + "\n" + ok + "\n", "more than one line"},
		{"two values", ok + " {}", "more than one JSON value"},
		{"no protocol", `{"detector":"transnetv2"}`, "no protocol stated"},
		{"protocol as text", `{"protocol":"1"}`, "not a JSON version object"},
		{"no version", strings.Replace(ok, `"0.1.0"`, `""`, 1), "no helper version stated"},
		{"no model", strings.Replace(ok, `"m.onnx"`, `""`, 1), "no model stated"},
		{"short sha", strings.Replace(ok, fakeSHA256, "0123", 1), "model_sha256 is not a SHA-256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseVersion(tc.output)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected an error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestDetect runs the correct fake on 2,500 frames: one probability per frame, in the order of the
// frames although the fake answers 100 frames late, the progress going up at a bounded rate to the
// total, the stderr lines forwarded to Debug.
func TestDetect(t *testing.T) {
	pidFile := useFake(t, "ok")
	const n = 2500
	var (
		reports []int
		debug   []string
	)
	probabilities, err, elapsed := detectAndCheck(t, t.Context(), pidFile, DetectConfig{
		Feed:           testFeed{n: n}.feed,
		Debug:          func(msg string) { debug = append(debug, msg) },
		RuntimeError:   func(err error) { t.Errorf("unexpected runtime error: %s", err) },
		ProgressReport: func(p int) { reports = append(reports, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(probabilities) != n {
		t.Fatalf("expected %d probabilities, got %d", n, len(probabilities))
	}
	for i, p := range probabilities {
		if want := fakeProbability(uint16(i)); p != want {
			t.Fatalf("frame %d: expected %v, got %v", i, want, p)
		}
	}
	if len(reports) == 0 || reports[len(reports)-1] != n {
		t.Errorf("expected the last report to be %d, got %v", n, reports)
	}
	for i := 1; i < len(reports); i++ {
		if reports[i] <= reports[i-1] {
			t.Errorf("reports not increasing: %v", reports)
			break
		}
	}
	if limit := int(elapsed/ProgressPeriod) + 2; len(reports) > limit {
		t.Errorf("%d reports in %v, at most %d expected", len(reports), elapsed, limit)
	}
	if len(debug) < 2 || !strings.HasPrefix(debug[0], "Detect shots with: ") || !strings.HasSuffix(debug[0], " detect") || debug[1] != "fake: model loaded" {
		t.Errorf("expected the command line then the fake's stderr line in Debug, got %q", debug)
	}
}

// TestDetectFailures checks that every failure ends the call with its real cause, the helper
// reaped and no goroutine left, whatever ends first.
func TestDetectFailures(t *testing.T) {
	errFeed := errors.New("ffmpeg failed")
	for _, tc := range []struct {
		name     string
		mode     string
		feed     testFeed
		wantErr  []string // substrings of the error
		wantIs   error    // errors.Is, if non nil
		wantExit int      // the exit status in the error (errors.As *exec.ExitError), if non zero
	}{
		{"too few lines", "few", testFeed{n: 250}, []string{"wrote 249 probabilities for 250 frames"}, nil, 0},
		{"too many lines", "many", testFeed{n: 250}, []string{"wrote 251 probabilities for 250 frames"}, nil, 0},
		{"not a number", "notnumber", testFeed{n: 250}, []string{`line 4 (frame 3): not a number: "abc"`}, nil, 0},
		{"out of range", "outofrange", testFeed{n: 250}, []string{`line 4 (frame 3): not a probability between 0 and 1: "1.5"`}, nil, 0},
		// the helper ends first: its failure, not the broken pipe
		{"exit before reading everything", "earlyexit", testFeed{n: 250},
			[]string{"stopped reading its frames after", "it failed: exit status 3", "fake: out of memory"}, nil, 3},
		{"exit 0 before reading everything", "exit0early", testFeed{n: 250},
			[]string{"stopped reading its frames after", "it exited with status 0"}, nil, 0},
		{"input closed, no exit", "closehang", testFeed{n: 250},
			[]string{"stopped reading its frames after", "did not exit within 300ms and was killed"}, nil, 0},
		{"exit after answering everything", "exitafter", testFeed{n: 250},
			[]string{"error during", "exit status 4", "fake: cleanup failed"}, nil, 4},
		// the feed ends first
		{"feed fails, helper reading", "ok", testFeed{n: 250, failAt: 120, failErr: errFeed},
			[]string{"frames feed failed after 120 frames: ffmpeg failed"}, errFeed, 0},
		{"feed fails, helper not reading", "hang", testFeed{n: 250, failAt: 10, failErr: errFeed},
			[]string{"frames feed failed after 10 frames: ffmpeg failed"}, errFeed, 0},
		{"feed reports more frames than written", "ok", testFeed{n: 250, lie: 1},
			[]string{"the frames feed reports 251 frames but wrote 972000 bytes"}, nil, 0},
		{"feed writes part of a frame", "ok", testFeed{n: 250, extra: 10},
			[]string{"the frames feed reports 250 frames but wrote 972010 bytes"}, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidFile := useFake(t, tc.mode)
			previousGrace := helperExitGrace
			defer func() { helperExitGrace = previousGrace }()
			helperExitGrace = 300 * time.Millisecond
			feed := tc.feed
			if feed.failErr != nil {
				feed.failAfter = pidFile
			}
			probabilities, err, elapsed := detectAndCheck(t, t.Context(), pidFile, DetectConfig{Feed: feed.feed})
			if err == nil {
				t.Fatalf("expected an error, got %d probabilities", len(probabilities))
			}
			if probabilities != nil {
				t.Errorf("expected no probabilities with an error, got %d", len(probabilities))
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected the error to contain %q, got %v", want, err)
				}
			}
			if strings.Contains(strings.ToLower(err.Error()), "broken pipe") {
				t.Errorf("the error names the broken pipe instead of its cause: %v", err)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("expected the error to wrap %v, got %v", tc.wantIs, err)
			}
			if tc.wantExit != 0 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.wantExit {
					t.Errorf("expected the helper's exit status %d in the error, got %v", tc.wantExit, err)
				}
			}
			if elapsed > 5*time.Second {
				t.Errorf("Detect took %v to fail", elapsed)
			}
		})
	}
}

// TestDetectCancel cancels the context while the feed, which ignores it, is blocked on a write to
// a helper which stopped reading (the fake reads 2 frames, and the pipe holds far fewer than the
// 250 written): the helper is killed, which unblocks the write, and the call returns the
// cancellation promptly.
func TestDetectCancel(t *testing.T) {
	pidFile := useFake(t, "hang")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	time.AfterFunc(300*time.Millisecond, cancel)
	_, err, elapsed := detectAndCheck(t, ctx, pidFile, DetectConfig{Feed: testFeed{n: 250, ignoreCtx: true}.feed})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected the cancellation, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Detect took %v to return after the cancellation", elapsed)
	}
}

func TestDetectNoFeed(t *testing.T) {
	if _, err := Detect(t.Context(), DetectConfig{}); err == nil {
		t.Error("expected an error without a feed")
	}
}

func TestParseProbability(t *testing.T) {
	for _, tc := range []struct {
		line string
		want float32
		ok   bool
	}{
		{"0", 0, true},
		{"1", 1, true},
		{"0.25\r", 0.25, true},
		{"1e-3", 0.001, true},
		{"0.100000001", 0.1, true},
		{"", 0, false},
		{"abc", 0, false},
		{"-0.1", 0, false},
		{"1.0000001", 0, false},
		{"NaN", 0, false},
		{"Inf", 0, false},
		{"0x1p-2", 0, false},
	} {
		p, err := parseProbability(tc.line)
		if tc.ok && (err != nil || p != tc.want) {
			t.Errorf("%q: expected %v, got %v, %v", tc.line, tc.want, p, err)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), strconv.Quote(tc.line))) {
			t.Errorf("%q: expected an error quoting the line, got %v, %v", tc.line, p, err)
		}
	}
}
