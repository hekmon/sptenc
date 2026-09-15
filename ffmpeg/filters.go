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

// GetFilters runs ffmpeg -filters and parses its output.
func GetFilters(ctx context.Context) (info FiltersInfo, err error) {
	args := []string{"-filters"}
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

// FiltersInfo holds the parsed output of ffmpeg -filters.
type FiltersInfo struct {
	VersionInfo
	Filters []Filter
}

// Has reports whether a filter with the given name is available.
func (fi *FiltersInfo) Has(name string) bool {
	for _, f := range fi.Filters {
		if f.Name == name {
			return true
		}
	}
	return false
}

// Filter represents a single filter entry from ffmpeg -filters.
type Filter struct {
	Flags       string // e.g. "TS" or ".."
	Name        string
	IO          string // e.g. "VV->V", "A->A", "|->V"
	Description string
}

// HasLibVMAF reports whether libvmaf is available.
func (fi *FiltersInfo) HasLibVMAF() bool { return fi.Has("libvmaf") }

// HasLibVMAFCUDA reports whether libvmaf_cuda is available.
func (fi *FiltersInfo) HasLibVMAFCUDA() bool { return fi.Has("libvmaf_cuda") }

var (
	filtersHeaderRe = regexp.MustCompile(`^\s*Filters:\s*$`)
	filtersSepRe    = regexp.MustCompile(`^\s*[-=]{3,}\s*$`)
	filterLineRe    = regexp.MustCompile(`^\s+(\S+)\s+(\S+)\s+([A-Z|]\S*->[A-Z|])\s+(.+)$`)
)

// Parse populates FiltersInfo from the raw ffmpeg -filters output.
func (fi *FiltersInfo) Parse(data []byte) error {
	// First, try to parse the version header.
	if err := fi.VersionInfo.Parse(data); err != nil {
		// Version header is optional for this parser.
		fi.VersionInfo = VersionInfo{}
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	inList := false
	for scanner.Scan() {
		line := scanner.Text()

		// Look for the "Filters:" header.
		if filtersHeaderRe.MatchString(line) {
			inList = false
			continue
		}

		// Skip legend lines (contain " = ") and separator lines.
		if strings.Contains(line, " = ") || filtersSepRe.MatchString(line) {
			inList = true
			continue
		}

		if !inList || strings.TrimSpace(line) == "" {
			continue
		}

		if m := filterLineRe.FindStringSubmatch(line); m != nil {
			fi.Filters = append(fi.Filters, Filter{
				Flags:       m[1],
				Name:        m[2],
				IO:          m[3],
				Description: m[4],
			})
		}
	}
	return scanner.Err()
}
