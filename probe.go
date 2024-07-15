package main

import (
	"fmt"

	"github.com/hekmon/liveprogress/v2"
	"github.com/hekmon/scenc/ffmpegutils"
)

func getStreamsInfos(path string) (stats ffmpegutils.FFProbeStats, err error) {
	// output fx
	bypass := liveprogress.Bypass()
	runtimeError := func(err error) {
		fmt.Fprintf(bypass, "%s\n", err)
	}
	var debugPrint func(string)
	if *debug {
		debugPrint = func(s string) {
			fmt.Fprintf(bypass, "%s\n", s)
		}
	}
	// execute
	return ffmpegutils.GetStreamsInfos(ffmpegutils.GetStreamConfig{
		Path:                path,
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
	})
}
