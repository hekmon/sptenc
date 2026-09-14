package ffmpeg

import (
	"fmt"
	"os"
	"runtime"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/processpriority"
	"github.com/shirou/gopsutil/v4/cpu"
)

const (
	float64Precision = 64 // standard float64 precision for formatting
)

var (
	// ProcessPriority will be applied to all commands ran by the ffmpeg package
	ProcessPriority = processpriority.BelowNormal
	// NbThreadsToUse is the number of CPU threads to use when auto threading can not be used
	NbThreadsToUse = runtime.NumCPU()
)

func init() {
	// Lower NbThreadsToUse to physical cores count instead of logical if possible
	physicalCores, err := cpu.Counts(false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Failed to recover physical CPU cores count (will default to %d): %s\n", NbThreadsToUse, err)
	} else if physicalCores > 0 {
		NbThreadsToUse = physicalCores
	}
}

func getPrintableCMDLine(program string, args []string) string {
	return shellescape.Quote(program) + " " + shellescape.QuoteCommand(args)
}

// GetEncoderQPRange returns the QP range for the given encoder
func GetEncoderQPRange(encoder Encoder) (qpMin, qpMax int, found bool) {
	switch encoder {
	case HEVCEncoderLibx265:
		return HEVCLibx265QPMin, HEVCLibx265QPMax, true
	case HEVCEncoderNVEnc:
		return HEVCNVEncQPMin, HEVCNVEncQPMax, true
	case HEVCEncoderVAAPI:
		return HEVCVAAPIQPMin, HEVCVAAPIQPMax, true
	case HEVCEncoderD3D12VA:
		return HEVCD3D12VAQPMin, HEVCD3D12VAQPMax, true
	case AV1EncoderLibaom:
		return AV1LibaomQPMin, AV1LibaomQPMax, true
	case AV1EncoderSVTAV1:
		return AV1SVTAV1QPMin, AV1SVTAV1QPMax, true
	case AV1EncoderNVEnc:
		return AV1NVEncQPMin, AV1NVEncQPMax, true
	case AV1EncoderVAAPI:
		return AV1VAAPIQPMin, AV1VAAPIQPMax, true
	default:
		return
	}
}
