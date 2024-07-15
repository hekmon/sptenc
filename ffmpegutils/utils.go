package ffmpegutils

import (
	"runtime"

	"github.com/hekmon/processpriority"
)

var (
	NbThreadsToUse  = runtime.NumCPU()
	ProcessPriority = processpriority.BelowNormal
)
