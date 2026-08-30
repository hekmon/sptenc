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
	Encoders []Encoder
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

// Encoder represents a single encoder entry from ffmpeg -encoders.
type Encoder struct {
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
			enc := Encoder{
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
