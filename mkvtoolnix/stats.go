package mkvtoolnix

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hekmon/processpriority"
)

type GenerateMKVStatsConfig struct {
	// Input
	Path string
	// Reporting
	Debug          func(msg string)
	RuntimeError   func(err error) // non fatal errors
	ProgressReport func(percent int)
}

func GenerateMKVStats(ctx context.Context, config GenerateMKVStatsConfig) (err error) {
	// Validate inputs
	if config.Path == "" {
		err = errors.New("input path cannot be empty")
		return
	}
	if filepath.Ext(config.Path) != ".mkv" {
		err = fmt.Errorf("file is not mkv")
		return
	}
	// Args
	args := []string{"--add-track-statistics-tags", config.Path}
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Generate MKV statistics tag with: %s \"%s\"", MKVPropEdit, strings.Join(args, "\" \"")))
	}
	cmd := exec.CommandContext(ctx, MKVPropEdit, args...)
	// Prepare output handling
	outputPipe, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stdout pipe: %w", err)
		return
	}
	// Start program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w", MKVPropEdit, err)
		return
	}
	// Start progress monitoring (after cmd.Start to avoid goroutine leak on error)
	progressDone := make(chan struct{})
	go func() {
		mkvPropStatsProgress(outputPipe, config.ProgressReport, config.RuntimeError)
		close(progressDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", MKVPropEdit, err))
	}
	<-progressDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w", MKVPropEdit, err)
		return
	}
	return
}

func mkvPropStatsProgress(mkvpropOutput io.ReadCloser, progress func(percent int), runtimeError func(error)) {
	output := bufio.NewReader(mkvpropOutput)
	var (
		err         error
		r           rune
		currentLine string
		percent     int
	)
	// Read rune by rune until EOF
	lineBuffer := bytes.NewBuffer(nil)
	for {
		// Read a rune a write it to our buffer
		if r, _, err = output.ReadRune(); err != nil {
			if !errors.Is(err, io.EOF) && runtimeError != nil {
				runtimeError(fmt.Errorf("error while reading rune from mkvpropstats output: %w", err))
			}
			return
		}
		lineBuffer.WriteRune(r)
		// Is this a complete line ?
		switch r {
		case '\n':
			lineBuffer.Reset()
		case '%':
			currentLine = lineBuffer.String()
			// We should have the line "Progression : xx%" or "Progress: xx%" before term line is cleared
			if percent, err = parseMKVPropStats(currentLine); err != nil {
				if runtimeError != nil {
					runtimeError(fmt.Errorf("error parsing mkvpropstats progress line: %s", err))
				}
			} else {
				if progress != nil {
					progress(percent)
				}
			}
			lineBuffer.Reset()
		}
	}
}

func parseMKVPropStats(line string) (percent int, err error) {
	elems := strings.Split(line, " ")
	return strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(elems[len(elems)-1]), "%"))
}
