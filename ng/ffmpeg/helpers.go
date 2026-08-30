package ffmpeg

import (
	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/processpriority"
)

const (
	float64Precision = 64 // standard float64 precision for formatting
)

var (
	// ProcessPriority will be applied to all commands ran by the ffmpeg package
	ProcessPriority = processpriority.BelowNormal
)

func getPrintableCMDLine(program string, args []string) string {
	return shellescape.Quote(program) + " " + shellescape.QuoteCommand(args)
}

func GetEncoderQPRange(encoder Encoder) (qpMin, qpMax int, found bool) {
	switch encoder {
	case HEVCEncoderLibx265, HEVCEncoderNVENC, HEVCEncoderVAAPI:
		return HEVCQPMin, HEVCQPMax, true
	case AV1EncoderLibaom:
		return AV1LibaomQPMin, AV1LibaomQPMax, true
	case AV1EncoderNVENC:
		return AV1NVENCQPMin, AV1NVENCQPMax, true
	case AV1EncoderVAAPI:
		return AV1VAAPIQPMin, AV1VAAPIQPMax, true
	default:
		return
	}
}
