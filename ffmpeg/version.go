package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// GetFFProbeVersion runs ffprobe -version, validates the binary is actually ffprobe, and parses its output.
func GetFFProbeVersion(ctx context.Context) (info VersionInfo, err error) {
	info, err = runVersion(ctx, FFProbeBinary)
	if err != nil {
		return
	}
	if info.Binary != "ffprobe" {
		err = fmt.Errorf("binary mismatch: expected ffprobe but got %s (check FFProbeBinary path)", info.Binary)
	}
	return
}

// GetFFMPEGVersion runs ffmpeg -version, validates the binary is actually ffmpeg, and parses its output.
func GetFFMPEGVersion(ctx context.Context) (info VersionInfo, err error) {
	info, err = runVersion(ctx, FFMPEGBinary)
	if err != nil {
		return
	}
	if info.Binary != "ffmpeg" {
		err = fmt.Errorf("binary mismatch: expected ffmpeg but got %s (check FFMPEGBinary path)", info.Binary)
	}
	return
}

func runVersion(ctx context.Context, binary string) (info VersionInfo, err error) {
	args := []string{"-version"}
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s", binary, err, getPrintableCMDLine(binary, args))
		return
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s", binary, err, stderr.String(), getPrintableCMDLine(binary, args))
		return
	}
	if err = info.Parse(stdout.Bytes()); err != nil {
		err = fmt.Errorf("error parsing %s output: %w", binary, err)
		return
	}
	return
}

// VersionInfo holds the parsed output of ffprobe -version.
type VersionInfo struct {
	// Binary is the name of the binary that produced the output, e.g. "ffprobe" or "ffmpeg".
	Binary string
	// Version is the FFmpeg version string, e.g. "n8.0.1".
	Version string
	// Copyright is the copyright notice, e.g. "2007-2025 the FFmpeg developers".
	Copyright string
	// Compiler is the compiler info line, e.g. "gcc 13 (Ubuntu 13.3.0-6ubuntu2~24.04.1)".
	Compiler string
	// Configuration is the raw configuration string (everything after "configuration: ").
	Configuration string
	// ConfigurationFlags holds the individual flags parsed from the configuration line.
	ConfigurationFlags []string
	// Libraries lists the versions of the linked FFmpeg libraries.
	Libraries []LibraryVersion
}

// LibraryVersion represents the version of a single FFmpeg library.
type LibraryVersion struct {
	Name         string
	CurrentMajor int
	CurrentMinor int
	CurrentMicro int
	LatestMajor  int
	LatestMinor  int
	LatestMicro  int
}

// String returns the library version in the same format as ffprobe output.
func (lv LibraryVersion) String() string {
	return fmt.Sprintf("%s %3d. %3d.%03d / %3d. %3d.%03d",
		lv.Name, lv.CurrentMajor, lv.CurrentMinor, lv.CurrentMicro,
		lv.LatestMajor, lv.LatestMinor, lv.LatestMicro)
}

var (
	versionLineRe = regexp.MustCompile(`^(ffprobe|ffmpeg) version (.+) Copyright \(c\) (.+)$`)
	builtLineRe   = regexp.MustCompile(`^built with (.+)$`)
	configLineRe  = regexp.MustCompile(`^configuration: (.+)$`)
	libVersionRe  = regexp.MustCompile(`^` +
		`(\S+)\s+` + // library name
		`(\d+)\.\s+(\d+)\.(\d+)\s*/\s*` + // current version
		`(\d+)\.\s+(\d+)\.(\d+)` + // latest version
		`$`)
)

// Parse populates VersionInfo from the raw ffprobe -version output.
func (vi *VersionInfo) Parse(data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if m := versionLineRe.FindStringSubmatch(line); m != nil {
			vi.Binary = m[1]
			vi.Version = m[2]
			vi.Copyright = m[3]
			continue
		}

		if m := builtLineRe.FindStringSubmatch(line); m != nil {
			vi.Compiler = m[1]
			continue
		}

		if m := configLineRe.FindStringSubmatch(line); m != nil {
			vi.Configuration = m[1]
			vi.ConfigurationFlags = parseConfigFlags(m[1])
			continue
		}

		if m := libVersionRe.FindStringSubmatch(line); m != nil {
			lib, err := parseLibraryVersion(m)
			if err != nil {
				return fmt.Errorf("failed to parse library version line %q: %w", line, err)
			}
			vi.Libraries = append(vi.Libraries, lib)
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if vi.Binary == "" {
		return errors.New("version line not found in output")
	}
	return nil
}

func parseConfigFlags(raw string) []string {
	var flags []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, r := range raw {
		switch {
		case inQuote:
			current.WriteRune(r)
			if r == quoteChar {
				inQuote = false
			}
		case r == '\'' || r == '"':
			current.WriteRune(r)
			inQuote = true
			quoteChar = r
		case r == ' ' || r == '\t':
			if current.Len() > 0 {
				flags = append(flags, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		flags = append(flags, current.String())
	}
	return flags
}

func parseLibraryVersion(matches []string) (LibraryVersion, error) {
	var lv LibraryVersion
	var err error
	lv.Name = matches[1]
	if lv.CurrentMajor, err = strconv.Atoi(matches[2]); err != nil {
		return lv, fmt.Errorf("current major: %w", err)
	}
	if lv.CurrentMinor, err = strconv.Atoi(matches[3]); err != nil {
		return lv, fmt.Errorf("current minor: %w", err)
	}
	if lv.CurrentMicro, err = strconv.Atoi(matches[4]); err != nil {
		return lv, fmt.Errorf("current micro: %w", err)
	}
	if lv.LatestMajor, err = strconv.Atoi(matches[5]); err != nil {
		return lv, fmt.Errorf("latest major: %w", err)
	}
	if lv.LatestMinor, err = strconv.Atoi(matches[6]); err != nil {
		return lv, fmt.Errorf("latest minor: %w", err)
	}
	if lv.LatestMicro, err = strconv.Atoi(matches[7]); err != nil {
		return lv, fmt.Errorf("latest micro: %w", err)
	}
	return lv, nil
}
