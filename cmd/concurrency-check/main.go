// Command concurrency-check tells whether an encoder gives the same output when two of its encodes
// run at the same time as when it encodes alone, with the encoder arguments of sptenc.
//
// sptenc searches several segments at once (-C) on the premise that it changes the time taken and
// nothing else. hevc_videotoolbox broke it on an Apple M4 Max: two encodes at once changed each
// other's keyframes, and sptenc runs its encodes one at a time since (BENCHMARKS.md, Two encodes at
// once). This check is for whoever wants to see what their own machine does, and to report when it
// contradicts what sptenc assumes, at https://github.com/hekmon/sptenc/issues:
//
//	go run ./cmd/concurrency-check -encoder hevc_videotoolbox
//
// It encodes the same clip alone several times, then two at a time several times, and compares
// every encode with the lone one. It exits with 2 when the result contradicts what sptenc assumes.
//
// # WHY A PROCESS PER ENCODE
//
// sptenc keeps its VideoToolbox encodes apart with a lock held in the process: two encodes started
// from the same process never overlap, and a check starting them so would never see anything. Each
// encode runs in a process of its own, this program started again, as the encodes of two sptenc
// runs at the same time would, and through the encoder adapter of sptenc: the arguments are the
// ones it uses.
//
// # WHAT IS COMPARED
//
// Each encode by its size, the checksum of its decoded pictures, and its numbers of frames and of
// keyframes. Not the stream itself: VideoToolbox's differs from one lone encode to the next, its
// pictures and its size do not.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/pipeline"
)

const (
	reportURL = "https://github.com/hekmon/sptenc/issues"
	// childFlag makes the program encode its input, alone, and exit (see WHY A PROCESS PER ENCODE)
	childFlag = "child-encode"
)

func main() {
	os.Exit(check())
}

// check runs the check, or one encode in a child process, and returns the exit code.
func check() int {
	var (
		encoder     = flag.String("encoder", "", "the encoder to check, as sptenc names it (hevc_videotoolbox, hevc_nvenc, hevc_vaapi, libx265...)")
		input       = flag.String("input", "", "the video to encode (default: a synthetic 1080p clip of 10 s)")
		runs        = flag.Int("runs", 10, "the lone encodes, and the rounds of two encodes at once")
		qp          = flag.Int("qp", -1, "the QP, on sptenc's scale for the encoder (default: the middle of its range)")
		nvidia      = flag.Int("nvidia-gpu-index", ffmpeg.CUDADefaultDevice, "NVIDIA GPU index, for NVENC")
		vaapi       = flag.String("vaapi-renderer-path", ffmpeg.VAAPIDefaultDevice, "DRM render node, for VA-API")
		d3d12       = flag.Int("d3d12va-gpu-index", ffmpeg.D3D12VADefaultDevice, "Direct3D 12 adapter index, for D3D12VA")
		ffmpegPath  = flag.String("ffmpeg-path", ffmpeg.FFMPEGBinary, "ffmpeg binary path")
		ffprobePath = flag.String("ffprobe-path", ffmpeg.FFProbeBinary, "ffprobe binary path")
		keep        = flag.Bool("keep", false, "keep the working directory, its encodes included")
		childOutput = flag.String(childFlag, "", "internal: encode -input to this file and exit")
	)
	flag.Parse()
	ffmpeg.FFMPEGBinary, ffmpeg.FFProbeBinary = *ffmpegPath, *ffprobePath
	adapter := &pipeline.EncoderAdapter{
		Encoder:           ffmpeg.Encoder(*encoder),
		NVIDIAGPUIndex:    *nvidia,
		VAAPIRendererPath: *vaapi,
		D3D12VAGPUIndex:   *d3d12,
	}
	ctx := context.Background()
	if *childOutput != "" {
		if err := adapter.Encode(ctx, *input, *childOutput, *qp, core.VideoStream{}, nil, nil, func(err error) {
			fmt.Fprintln(os.Stderr, err)
		}); err != nil {
			return failure(err)
		}
		return 0
	}
	qpMin, qpMax, found := ffmpeg.GetEncoderQPRange(adapter.Encoder)
	switch {
	case !found:
		return failure(fmt.Errorf("unknown encoder %q: -encoder takes a name sptenc knows (hevc_videotoolbox, hevc_nvenc, hevc_vaapi, libx265...)", *encoder))
	case *runs < 1:
		return failure(fmt.Errorf("-runs must be 1 or more, got %d", *runs))
	case *qp == -1:
		*qp = (qpMin + qpMax) / 2
	case *qp < qpMin || *qp > qpMax:
		return failure(fmt.Errorf("-qp must be within the range of %s, %d to %d, got %d", *encoder, qpMin, qpMax, *qp))
	}
	workingDir, err := os.MkdirTemp("", "sptenc-concurrency-check-")
	if err != nil {
		return failure(err)
	}
	failed := true // a failed check leaves its working directory for inspection
	defer func() {
		if *keep || failed {
			fmt.Printf("Working directory left: %s\n", workingDir)
		} else {
			os.RemoveAll(workingDir)
		}
	}()
	if *input == "" {
		*input = filepath.Join(workingDir, "synthetic.mkv")
		if err = run(ffmpeg.FFMPEGBinary, "-y", "-loglevel", "error", "-f", "lavfi", "-i",
			"testsrc2=size=1920x1080:rate=24000/1001:duration=10,noise=alls=12:allf=t",
			"-c:v", "ffv1", "-pix_fmt", "yuv420p10le", *input); err != nil {
			return failure(fmt.Errorf("failed to make the synthetic clip: %w", err))
		}
	}
	self, err := os.Executable()
	if err != nil {
		return failure(err)
	}
	encode := func(output string) error {
		return run(self, "-encoder", *encoder, "-qp", strconv.Itoa(*qp), "-input", *input,
			"-nvidia-gpu-index", strconv.Itoa(*nvidia), "-vaapi-renderer-path", *vaapi,
			"-d3d12va-gpu-index", strconv.Itoa(*d3d12), "-ffmpeg-path", *ffmpegPath, "-ffprobe-path", *ffprobePath,
			"-"+childFlag, output)
	}
	version, _ := ffmpeg.GetFFMPEGVersion(ctx)
	fmt.Printf("%s at QP %d on %s, ffmpeg %s, %s/%s\n", *encoder, *qp, *input, version.Version, runtime.GOOS, runtime.GOARCH)
	// Alone, one after the other
	fmt.Printf("\n%d encodes alone:\n", *runs)
	alone := make([]encodeResult, *runs)
	for i := range alone {
		output := filepath.Join(workingDir, fmt.Sprintf("alone_%02d.mkv", i+1))
		if err = encode(output); err != nil {
			return failure(fmt.Errorf("lone encode %d failed: %w", i+1, err))
		}
		if alone[i], err = describe(output); err != nil {
			return failure(err)
		}
		fmt.Printf("  %s\n", alone[i])
	}
	// Two at a time
	fmt.Printf("\n%d rounds of two encodes at once:\n", *runs)
	together := make([]encodeResult, 2*(*runs))
	for round := range *runs {
		var (
			wg   sync.WaitGroup
			errs [2]error
		)
		for j := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[j] = encode(filepath.Join(workingDir, fmt.Sprintf("together_%02d_%d.mkv", round+1, j+1)))
			}()
		}
		wg.Wait()
		for j := range 2 {
			if errs[j] != nil {
				return failure(fmt.Errorf("encode %d of round %d failed: %w", j+1, round+1, errs[j]))
			}
			if together[2*round+j], err = describe(filepath.Join(workingDir, fmt.Sprintf("together_%02d_%d.mkv", round+1, j+1))); err != nil {
				return failure(err)
			}
			fmt.Printf("  %s\n", together[2*round+j])
		}
	}
	failed = false
	summary, contradicts := verdict(adapter.Encoder, alone, together)
	fmt.Printf("\n%s\n", summary)
	if contradicts {
		fmt.Printf("\nThis contradicts what sptenc assumes: please report it at %s, with this output, your machine and its OS.\n", reportURL)
		return 2
	}
	return 0
}

// encodeResult describes an encode: two encodes are the same when all of it is (see WHAT IS COMPARED).
type encodeResult struct {
	size      int64
	pictures  string // MD5 of the decoded pictures
	frames    int
	keyframes int
}

func (er encodeResult) String() string {
	return fmt.Sprintf("%d B, pictures %s, %d frames, %d keyframes", er.size, er.pictures, er.frames, er.keyframes)
}

// describe measures an encode: its size, its pictures decoded (by the software decoder), its frames.
func describe(path string) (er encodeResult, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	er.size = info.Size()
	md5, err := exec.Command(ffmpeg.FFMPEGBinary, "-loglevel", "error", "-i", path, "-map", "0:v:0", "-f", "md5", "-").Output()
	if err != nil {
		return er, fmt.Errorf("failed to decode %s: %w", path, err)
	}
	er.pictures = strings.TrimPrefix(strings.TrimSpace(string(md5)), "MD5=")
	flags, err := exec.Command(ffmpeg.FFProbeBinary, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=flags", "-of", "csv=p=0", path).Output()
	if err != nil {
		return er, fmt.Errorf("failed to read the packets of %s: %w", path, err)
	}
	for _, packet := range strings.Fields(string(flags)) {
		er.frames++
		if strings.Contains(packet, "K") {
			er.keyframes++
		}
	}
	return
}

// verdict compares the encodes made two at a time with the lone ones, against what sptenc assumes
// of the encoder: VideoToolbox's encodes disturb each other when they run at once, which is why
// sptenc runs them one at a time, the other encoders' do not. The lone encodes must all be the
// same: the search of sptenc assumes that an encoder gives the same output for the same input.
func verdict(encoder ffmpeg.Encoder, alone, together []encodeResult) (summary string, contradicts bool) {
	for _, er := range alone[1:] {
		if er != alone[0] {
			return "The lone encodes are not all the same: the QP search of sptenc assumes that an encoder gives the same output for the same input.", true
		}
	}
	var differ int
	for _, er := range together {
		if er != alone[0] {
			differ++
		}
	}
	disturbed := encoder == ffmpeg.HEVCEncoderVideoToolbox
	switch {
	case differ > 0 && disturbed:
		return fmt.Sprintf("%d of the %d encodes made two at a time differ from the lone one: they disturb each other, as sptenc assumes of %s, whose encodes it runs one at a time.",
			differ, len(together), encoder), false
	case differ > 0:
		return fmt.Sprintf("%d of the %d encodes made two at a time differ from the lone one: they disturb each other, which sptenc does not expect of %s. With -C above 1, its output depends on it.",
			differ, len(together), encoder), true
	case disturbed:
		return fmt.Sprintf("Every encode made two at a time is the lone one: sptenc runs the encodes of %s one at a time, which this machine does not need.",
			encoder), true
	default:
		return fmt.Sprintf("Every encode made two at a time is the lone one, as sptenc assumes of %s: -C only changes the time taken.", encoder), false
	}
}

// run starts a program, its output on ours, and waits for it.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// failure prints an error and returns the exit code of a failed check.
func failure(err error) int {
	fmt.Fprintln(os.Stderr, err)
	return 1
}
