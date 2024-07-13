package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hekmon/cunits/v2"
)

func getWorkingDirPath() string {
	return filepath.Join(os.TempDir(), "scenc")
}

type ffmpegProgressStats struct {
	currentFrame int
	fps          float64
	dup          int         // images extract only
	drop         int         // images extract only
	size         cunits.Bits // encode only
	bitrate      string      // encode only
	speed        float64
}

func ffmpegProgressStatsParse(line string) (stats ffmpegProgressStats, err error) {
	// fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", line)
	var currentKey string
	parse := func(value string) (err error) {
		switch currentKey {
		case "frame":
			if stats.currentFrame, err = strconv.Atoi(value); err != nil {
				err = fmt.Errorf("error parsing value for current key %q: %w", currentKey, err)
			}
		case "fps":
			if stats.fps, err = strconv.ParseFloat(value, 64); err != nil {
				err = fmt.Errorf("error parsing value for current key %q: %w", currentKey, err)
			}
		case "dup":
			if stats.dup, err = strconv.Atoi(value); err != nil {
				err = fmt.Errorf("error parsing value for current key %q: %w", currentKey, err)
			}
		case "drop":
			if stats.drop, err = strconv.Atoi(value); err != nil {
				err = fmt.Errorf("error parsing value for current key %q: %w", currentKey, err)
			}
		case "size":
			if value != "N/A" {
				value = strings.ReplaceAll(value, "kB", "KB") // unix fix
				if stats.size, err = cunits.Parse(value); err != nil {
					err = fmt.Errorf("error parsing value for current key %q value %q: %w", currentKey, value, err)
				}
			}
		case "bitrate":
			stats.bitrate = value
		case "speed":
			if stats.speed, err = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "x"), 64); err != nil {
				err = fmt.Errorf("error parsing value for current key  %q: %w", currentKey, err)
			}
			// default:
			// 	fmt.Fprintf(liveprogress.Bypass(), "skipping unrecognized key %s\n", currentKey)
		}
		return
	}
	var tmp strings.Builder
	for _, r := range line {
		tmp.WriteRune(r)
		if r == '=' {
			data := strings.TrimSuffix(strings.TrimSpace(tmp.String()), "=")
			parts := strings.Split(data, " ")
			switch len(parts) {
			case 1:
				currentKey = parts[0]
			case 2:
				if err = parse(parts[0]); err != nil {
					return
				}
				currentKey = parts[1]
			default:
				if strings.HasPrefix(data, "N/A") && strings.HasSuffix(data, "frame") {
					// Some Weird output seen in the wild (on windows only)
					return
				}
				encodedData, _ := json.Marshal(data)
				err = fmt.Errorf("unexpected slicing size %d: %s\n", len(parts), string(encodedData))
				return
			}
			tmp.Reset()
		}
	}
	err = parse(tmp.String())
	return
}
