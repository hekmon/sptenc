package transnetv2

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hekmon/processpriority"
)

var (
	// ProgressPeriod is the shortest time between two calls of DetectConfig.ProgressReport (the
	// final one aside), the value of ffmpeg.StatsPeriod.
	ProgressPeriod = 100 * time.Millisecond
	// helperExitGrace is how long Detect waits for a helper that stopped reading its frames to
	// exit by itself, before it kills it: see WHY THE HELPER'S FAILURE OUTRANKS THE FEED'S.
	helperExitGrace = 5 * time.Second
)

// stderrTailLines is the number of the helper's last stderr lines an error quotes.
const stderrTailLines = 20

// Feed writes the frames of a source to frames, in order, each FrameSize bytes of RGB24 at
// FrameWidth x FrameHeight, and returns the number of frames written (a thin wrapper of
// ffmpeg.FeedFrames). It must stop when ctx is done, and give up when a write fails.
type Feed func(ctx context.Context, frames io.Writer) (int, error)

// DetectConfig holds the configuration for Detect.
type DetectConfig struct {
	// Input: the frames given to the helper
	Feed Feed
	// Reporting
	Debug        func(msg string) // the command line, and every stderr line of the helper
	RuntimeError func(err error)  // non fatal errors
	// ProgressReport is given the number of probabilities received so far, at most once per
	// ProgressPeriod, and the total once the helper is done. It is called from another goroutine
	// than Detect's, never after Detect returned.
	ProgressReport func(probabilities int)
}

// errHelperStuck stops a helper that stopped reading its frames without exiting.
var errHelperStuck = errors.New("the helper stopped reading its frames but did not exit")

// Detect runs the helper's detect command on the frames of config.Feed and returns one probability
// per frame, in order (see the protocol in the package documentation).
//
// The feed is given the helper's standard input and the context the helper runs under, while the
// probabilities are read as the helper writes them; the input is closed when the feed returns,
// and the helper is waited for. It fails:
//   - when ctx is done: the helper is killed (which makes a write of the feed blocked on a helper
//     not reading fail), and the error is the cancellation (errors.Is ctx's cause);
//   - when a line of the helper is not a number, or not between 0 and 1: the helper is killed and
//     the error quotes the line;
//   - when the helper stops reading its frames (it exited, failed or closed its input) before the
//     feed is done: the error is the helper's, its exit status and its last stderr lines, not the
//     feed's broken pipe (see WHY THE HELPER'S FAILURE OUTRANKS THE FEED'S);
//   - when the feed fails otherwise: the helper is killed and the error wraps the feed's;
//   - when the helper fails after reading every frame: its exit status and its last stderr lines;
//   - when the feed did not write the number of frames it reports, or the helper did not write
//     one probability per frame: the error gives both numbers.
//
// Whatever the order in which the feed and the helper end, the helper is reaped and every
// goroutine Detect started has ended when it returns.
//
// # WHY THE HELPER'S FAILURE OUTRANKS THE FEED'S
//
// The feed's writes fail only when the helper stopped reading: it exited, crashed or closed its
// input. Its error is then a broken pipe, which says nothing of why, while the helper's exit
// status and its stderr do. So a failed write (recorded by the writer handed to the feed, whatever
// the feed makes of its error) makes the helper the culprit, and Detect waits for it to exit by
// itself, so that its own exit status is the one reported, not the one of a kill: a helper which
// closed its input is about to exit. Only when it has not after helperExitGrace is it killed, and
// the error says so.
func Detect(ctx context.Context, config DetectConfig) (probabilities []float32, err error) {
	// Validate inputs
	if config.Feed == nil {
		err = errors.New("the frames feed cannot be nil")
		return
	}
	// Prepare command
	args := []string{"detect"}
	cmdLine := getPrintableCMDLine(Helper, args)
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Detect shots with: %s", cmdLine))
	}
	// The helper is killed when ctx is done, and when the call stops it for a cause of its own
	cmdCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	cmd := exec.CommandContext(cmdCtx, Helper, args...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stdin pipe: %w", err)
		return
	}
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
		err = fmt.Errorf("error starting %s: %w\n%s", Helper, err, cmdLine)
		return
	}
	// Start monitoring goroutines (after cmd.Start to avoid goroutine leak on error)
	var (
		readErr    error
		stdoutDone = make(chan struct{})
		stderrDone = make(chan struct{})
		tail       = &stderrTail{}
	)
	go func() {
		defer close(stdoutDone)
		if probabilities, readErr = readProbabilities(stdoutPipe, config.ProgressReport); readErr != nil {
			stop(readErr) // the output is refused: no need to run the rest
		}
	}()
	go func() {
		defer close(stderrDone)
		tail.read(stderrPipe, config.Debug, config.RuntimeError)
	}()
	if prioErr := processpriority.Set(cmd.Process.Pid, ProcessPriority); prioErr != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", Helper, prioErr))
	}
	// Feed the frames, then close the input: the helper writes its last probabilities at its end
	input := &helperInput{w: stdinPipe}
	frames, feedErr := config.Feed(cmdCtx, input)
	_ = stdinPipe.Close() // closing the write end of a pipe can not fail on a reader's account
	written, writeErr := input.state()
	switch {
	case writeErr != nil:
		// the helper stopped reading: let it exit by itself, see WHY THE HELPER'S FAILURE OUTRANKS
		// THE FEED'S (stdout ends when it exits)
		timer := time.NewTimer(helperExitGrace)
		select {
		case <-stdoutDone:
		case <-timer.C:
			stop(errHelperStuck)
		}
		timer.Stop()
	case feedErr != nil:
		stop(feedErr)
	}
	<-stdoutDone
	<-stderrDone
	runErr := cmd.Wait()
	// Interpret
	switch {
	case ctx.Err() != nil:
		err = fmt.Errorf("%s detection interrupted after %d frames fed: %w", Helper, frames, context.Cause(ctx))
	case readErr != nil:
		err = fmt.Errorf("error reading %s output: %w\n%s\n%s", Helper, readErr, tail, cmdLine)
	case writeErr != nil:
		var status error
		switch {
		case context.Cause(cmdCtx) == errHelperStuck:
			status = fmt.Errorf("it did not exit within %s and was killed", helperExitGrace)
		case runErr != nil:
			status = fmt.Errorf("it failed: %w", runErr)
		default:
			status = errors.New("it exited with status 0")
		}
		err = fmt.Errorf("%s stopped reading its frames after %d bytes (%d frames): %w\n%s\n%s",
			Helper, written, written/FrameSize, status, tail, cmdLine)
	case feedErr != nil:
		err = fmt.Errorf("frames feed failed after %d frames: %w", frames, feedErr)
	case runErr != nil:
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s", Helper, runErr, tail, cmdLine)
	case written != int64(frames)*FrameSize:
		err = fmt.Errorf("the frames feed reports %d frames but wrote %d bytes (%d frames of %d bytes)",
			frames, written, frames, FrameSize)
	case len(probabilities) != frames:
		err = fmt.Errorf("%s wrote %d probabilities for %d frames\n%s", Helper, len(probabilities), frames, cmdLine)
	}
	if err != nil {
		probabilities = nil
	}
	return
}

// helperInput is the helper's standard input as given to the feed: it records the bytes written
// and the first write error, whatever the feed makes of it.
type helperInput struct {
	w       io.Writer
	mu      sync.Mutex
	written int64
	err     error
}

func (in *helperInput) Write(p []byte) (n int, err error) {
	n, err = in.w.Write(p)
	in.mu.Lock()
	in.written += int64(n)
	if err != nil && in.err == nil {
		in.err = err
	}
	in.mu.Unlock()
	return
}

func (in *helperInput) state() (written int64, err error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.written, in.err
}

// readProbabilities reads the helper's output, one probability per line, until its end. It stops
// at the first line refused.
func readProbabilities(r io.Reader, report func(int)) (probabilities []float32, err error) {
	scanner := bufio.NewScanner(r)
	var lastReport time.Time
	lastReported := -1
	for scanner.Scan() {
		var p float32
		if p, err = parseProbability(scanner.Text()); err != nil {
			err = fmt.Errorf("line %d (frame %d): %w", len(probabilities)+1, len(probabilities), err)
			return
		}
		probabilities = append(probabilities, p)
		if report != nil && time.Since(lastReport) >= ProgressPeriod {
			report(len(probabilities))
			lastReport, lastReported = time.Now(), len(probabilities)
		}
	}
	if err = scanner.Err(); err != nil {
		return
	}
	if report != nil && lastReported != len(probabilities) {
		report(len(probabilities))
	}
	return
}

// parseProbability parses one line of the helper: the decimal text of a 32-bit float between 0
// and 1, bounds included.
func parseProbability(line string) (p float32, err error) {
	text := strings.TrimSpace(line)
	if strings.ContainsAny(text, "xX") { // strconv reads hexadecimal floats, the protocol states decimal
		return 0, fmt.Errorf("not a decimal number: %q", line)
	}
	value, err := strconv.ParseFloat(text, 32)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", line)
	}
	p = float32(value)
	if !(p >= 0 && p <= 1) { // NaN included
		return 0, fmt.Errorf("not a probability between 0 and 1: %q", line)
	}
	return p, nil
}

// stderrTail forwards the helper's stderr lines and keeps the last ones for the errors.
type stderrTail struct {
	lines []string
}

func (t *stderrTail) read(r io.Reader, debug func(string), runtimeError func(error)) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if debug != nil {
			debug(line)
		}
		if len(t.lines) == stderrTailLines {
			t.lines = append(t.lines[:0], t.lines[1:]...)
		}
		t.lines = append(t.lines, line)
	}
	if err := scanner.Err(); err != nil {
		if runtimeError != nil {
			runtimeError(fmt.Errorf("error reading %s stderr: %w", Helper, err))
		}
		// the helper must never block on a stderr nobody reads
		_, _ = io.Copy(io.Discard, r)
	}
}

// String returns the last lines kept, read only once read has returned.
func (t *stderrTail) String() string {
	if len(t.lines) == 0 {
		return "(nothing on stderr)"
	}
	return strings.Join(t.lines, "\n")
}
