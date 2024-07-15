package ffmpegutils

import "runtime"

var (
	NbThreadsToUse = runtime.NumCPU()
)
