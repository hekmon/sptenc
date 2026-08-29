package ffmpeg

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/cunits/v3"
)

// ProgressStats holds the parsed progress information from an ffmpeg encode or extraction.
type ProgressStats struct {
	CurrentFrame int
	FPS          float64
	Dup          int         // images extract only
	Drop         int         // images extract only
	Size         cunits.Bits // encode only
	Time         time.Duration
	Bitrate      string // encode only
	Speed        float64
}

func standardProgress(ffmpegOutput io.ReadCloser, progress func(stats ProgressStats), runtimeError func(error)) {
	output := bufio.NewReader(ffmpegOutput)
	var (
		err         error
		r           rune
		currentLine string
		stats       ProgressStats
	)
	// Read rune by rune until EOF
	lineBuffer := bytes.NewBuffer(nil)
	for {
		// Read a rune a write it to our buffer
		if r, _, err = output.ReadRune(); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			if runtimeError != nil {
				runtimeError(fmt.Errorf("error while reading rune from ffmpeg output: %w", err))
			}
			return
		}
		lineBuffer.WriteRune(r)
		// Is this a complete line ?
		switch r {
		case '\n':
			lineBuffer.Reset()
		case 'x':
			currentLine = lineBuffer.String()
			if !strings.Contains(currentLine, "speed=") {
				continue
			}
			// We are near the end of a line (speed=00.0x) before line clear
			if stats, err = parseProgressStats(strings.ReplaceAll(currentLine, "\r", "")); err != nil {
				if runtimeError != nil {
					runtimeError(fmt.Errorf("error parsing ffmpeg progress line: %s", err))
				}
			} else {
				if progress != nil {
					progress(stats)
				}
			}
			lineBuffer.Reset()
		}
	}
}

func parseProgressStats(line string) (stats ProgressStats, err error) {
	var currentKey string
	parse := func(value string) (err error) {
		if strings.Contains(value, "N/A") {
			return
		}
		switch currentKey {
		case "frame":
			if stats.CurrentFrame, err = strconv.Atoi(value); err != nil {
				err = fmt.Errorf("error parsing value for current key %q: %w", currentKey, err)
			}
		case "fps":
			if stats.FPS, err = strconv.ParseFloat(value, float64Precision); err != nil {
				err = fmt.Errorf("error parsing value for current key %q: %w", currentKey, err)
			}
		case "dup":
			if stats.Dup, err = strconv.Atoi(value); err != nil {
				err = fmt.Errorf("error parsing value for current key %q: %w", currentKey, err)
			}
		case "drop":
			if stats.Drop, err = strconv.Atoi(value); err != nil {
				err = fmt.Errorf("error parsing value for current key %q: %w", currentKey, err)
			}
		case "size":
			value = strings.ReplaceAll(value, "kB", "KB") // unix fix
			if stats.Size, err = cunits.Parse(value); err != nil {
				err = fmt.Errorf("error parsing value for current key %q value %q: %w", currentKey, value, err)
			}
		case "time":
			timeFields := strings.Split(value, ":")
			if len(timeFields) != 3 {
				err = fmt.Errorf("invalid number of fields for %s key: expecting 3 got %d: %s", currentKey, len(timeFields), value)
				return
			}
			var (
				hours, minutes int
				seconds        float64
			)
			if hours, err = strconv.Atoi(timeFields[0]); err != nil {
				err = fmt.Errorf("failed to parse hours in current %s key: %w", currentKey, err)
				return
			}
			if minutes, err = strconv.Atoi(timeFields[1]); err != nil {
				err = fmt.Errorf("failed to parse minutes in current %s key: %w", currentKey, err)
				return
			}
			if seconds, err = strconv.ParseFloat(timeFields[2], float64Precision); err != nil {
				err = fmt.Errorf("failed to parse seconds in current %s key: %w", currentKey, err)
				return
			}
			stats.Time = time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second
		case "bitrate":
			stats.Bitrate = value
		case "speed":
			if stats.Speed, err = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "x"), float64Precision); err != nil {
				err = fmt.Errorf("error parsing value for current key  %q: %w", currentKey, err)
			}
			// default:
			// 	fmt.Fprintf(liveprogress.Bypass(), "skipping unrecognized key %s", currentKey)
		}
		return
	}
	var tmp strings.Builder
	for _, r := range line {
		tmp.WriteRune(r)
		if r == '=' {
			data := strings.TrimSuffix(strings.TrimSpace(tmp.String()), "=")
			parts := strings.Fields(data)
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
				if data == "N/A    \rsize" {
					// can happen at the encode of an encode
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
