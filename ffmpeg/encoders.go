package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Encoder is an FFmpeg encoder identifier (e.g. "libx265").
type Encoder string

/*
 * NVENC shared resources
 * Used by both HEVC and AV1 hardware encoders.
 */

// NVEncEncodingPreset represents the encoding preset for NVIDIA NVENC encoders.
// The p1-p7 presets trade encoding speed for compression efficiency.
type NVEncEncodingPreset string

const (
	// NVEncPresetP1 is the fastest preset with the lowest compression efficiency.
	NVEncPresetP1 NVEncEncodingPreset = "p1"
	// NVEncPresetP2 offers very fast encoding at the cost of file size.
	NVEncPresetP2 NVEncEncodingPreset = "p2"
	// NVEncPresetP3 provides fast encoding with moderate compression.
	NVEncPresetP3 NVEncEncodingPreset = "p3"
	// NVEncPresetP4 is the default preset balancing speed and compression.
	NVEncPresetP4 NVEncEncodingPreset = "p4"
	// NVEncPresetP5 is slower than the default with slightly better compression.
	NVEncPresetP5 NVEncEncodingPreset = "p5"
	// NVEncPresetP6 provides significantly better compression than p5.
	NVEncPresetP6 NVEncEncodingPreset = "p6"
	// NVEncPresetP7 is the slowest preset with the best compression efficiency.
	NVEncPresetP7 NVEncEncodingPreset = "p7"

	// CUDADefaultDevice is the default CUDA device index.
	CUDADefaultDevice = 0

	nvEncSpatialAQ    = 1  // enable spatial adaptive quantization
	nvEncTemporalAQ   = 1  // enable temporal adaptive quantization
	nvEncMaxLookahead = 32 // max frames buffered for adaptive I/B decisions (iadapt/badapt): in constqp this is a pure compression win, same QP for smaller files (verified effective even in lossless: at 0 the driver falls back to all-intra)
)

// GetEncoders runs ffmpeg -encoders and parses its output.
func GetEncoders(ctx context.Context) (info EncodersInfo, err error) {
	args := []string{"-encoders"}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s", FFMPEGBinary, err, stderr.String(), getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	if err = info.Parse(stdout.Bytes()); err != nil {
		err = fmt.Errorf("error parsing %s output: %w", FFMPEGBinary, err)
		return
	}
	return
}

// EncodersInfo holds the parsed output of ffmpeg -encoders.
type EncodersInfo struct {
	VersionInfo
	Encoders []EncoderProfile
}

// Has reports whether an encoder with the given name is available.
func (ei *EncodersInfo) Has(name string) bool {
	for _, e := range ei.Encoders {
		if e.Name == name {
			return true
		}
	}
	return false
}

// EncoderProfile represents a single encoder entry from ffmpeg -encoders.
type EncoderProfile struct {
	Flags       string // e.g. "V....D" or "A....D"
	Name        string
	Description string
	Codec       string // Populated if the description ends with "(codec xxx)"
}

var (
	encodersHeaderRe = regexp.MustCompile(`^\s*Encoders:\s*$`)
	encodersSepRe    = regexp.MustCompile(`^\s*[-=]{3,}\s*$`)
	encoderLineRe    = regexp.MustCompile(`^\s+([A-Z.]{6})\s+(\S+)\s+(.+)$`)
	codecAliasRe     = regexp.MustCompile(`\(codec\s+(\S+)\)$`)
)

// Parse populates EncodersInfo from the raw ffmpeg -encoders output.
func (ei *EncodersInfo) Parse(data []byte) error {
	// First, try to parse the version header.
	if err := ei.VersionInfo.Parse(data); err != nil {
		ei.VersionInfo = VersionInfo{}
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	inList := false
	for scanner.Scan() {
		line := scanner.Text()

		// Look for the "Encoders:" header.
		if encodersHeaderRe.MatchString(line) {
			inList = false
			continue
		}

		// Skip legend lines (contain " = ") and separator lines.
		if strings.Contains(line, " = ") || encodersSepRe.MatchString(line) {
			inList = true
			continue
		}

		if !inList || strings.TrimSpace(line) == "" {
			continue
		}

		if m := encoderLineRe.FindStringSubmatch(line); m != nil {
			enc := EncoderProfile{
				Flags:       m[1],
				Name:        m[2],
				Description: strings.TrimSpace(m[3]),
			}
			// Extract codec alias if present.
			if ca := codecAliasRe.FindStringSubmatch(enc.Description); ca != nil {
				enc.Codec = ca[1]
			}
			ei.Encoders = append(ei.Encoders, enc)
		}
	}
	return scanner.Err()
}
