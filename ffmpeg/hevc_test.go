package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// TestHEVCVideoToolboxEncodeQPOneAtATime checks that the hevc_videotoolbox encodes of the process
// never overlap (see HEVCVideoToolboxEncodeQP), through an ffmpeg that fails when another one runs.
func TestHEVCVideoToolboxEncodeQPOneAtATime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake ffmpeg is a shell script")
	}
	dir := t.TempDir()
	running := filepath.Join(dir, "running")
	fake := filepath.Join(dir, "ffmpeg")
	// mkdir is atomic: it fails while another fake ffmpeg holds the directory
	script := fmt.Sprintf("#!/bin/sh\nmkdir %q 2>/dev/null || exit 3\nsleep 0.3\nrmdir %q\n", running, running)
	if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	// The fake does fail one of two runs at once
	first, second := exec.Command(fake), exec.Command(fake)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	firstErr, secondErr := first.Wait(), second.Wait()
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("the fake ffmpeg should fail exactly one of two runs at once, got %v and %v", firstErr, secondErr)
	}
	// Encodes started together run one after the other
	defer func(binary string) { FFMPEGBinary = binary }(FFMPEGBinary)
	FFMPEGBinary = fake
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = HEVCVideoToolboxEncodeQP(context.Background(), HEVCVideoToolboxEncodeQPConfig{
				// the input is only probed, and its probe failing is not an error (software decode)
				Input:        filepath.Join(dir, "segment.mkv"),
				Output:       filepath.Join(dir, fmt.Sprintf("encode_%d.mkv", i)),
				Quantization: 39,
				RuntimeError: func(error) {},
			})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("encode %d ran at the same time as another one: %v", i, err)
		}
	}
}
