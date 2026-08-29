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
