package transnetv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// VersionInfo holds the version line of the helper.
type VersionInfo struct {
	Protocol    int    `json:"protocol"`
	Detector    string `json:"detector"`
	Version     string `json:"version"`      // the helper's own version
	Model       string `json:"model"`        // the file name of the model it runs
	ModelSHA256 string `json:"model_sha256"` // the SHA-256 of that file, in hexadecimal
}

// GetVersion runs the helper's version command and returns its version line. It fails when the
// helper can not be run or fails, prints anything but one JSON object on one line, states another
// protocol than ProtocolVersion or another detector than DetectorName, or leaves a field empty;
// the error says which.
func GetVersion(ctx context.Context) (info VersionInfo, err error) {
	args := []string{"version"}
	cmd := exec.CommandContext(ctx, Helper, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s", Helper, err, getPrintableCMDLine(Helper, args))
		return
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s", Helper, err, stderr.String(), getPrintableCMDLine(Helper, args))
		return
	}
	if info, err = parseVersion(stdout.String()); err != nil {
		err = fmt.Errorf("error parsing %s output: %w\n%s", Helper, err, getPrintableCMDLine(Helper, args))
	}
	return
}

var sha256Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func parseVersion(output string) (info VersionInfo, err error) {
	line := strings.TrimRight(output, "\r\n")
	if line == "" {
		return info, errors.New("no version line")
	}
	if strings.ContainsAny(line, "\r\n") {
		return info, fmt.Errorf("more than one line: %q", output)
	}
	// # WHY THE PROTOCOL IS CHECKED FIRST
	//
	// Another protocol can name or shape its fields otherwise: its number is the only thing this
	// package can read from it, and the reason to give.
	decoder := json.NewDecoder(strings.NewReader(line))
	if err = decoder.Decode(&info); err != nil {
		return VersionInfo{}, fmt.Errorf("not a JSON version object: %q: %w", line, err)
	}
	if decoder.More() {
		return VersionInfo{}, fmt.Errorf("more than one JSON value: %q", line)
	}
	switch {
	case info.Protocol == 0:
		err = fmt.Errorf("no protocol stated: %q", line)
	case info.Protocol != ProtocolVersion:
		err = fmt.Errorf("protocol %d stated, this sptenc speaks protocol %d: %q", info.Protocol, ProtocolVersion, line)
	case info.Detector != DetectorName:
		err = fmt.Errorf("detector %q stated, expected %q: %q", info.Detector, DetectorName, line)
	case info.Version == "":
		err = fmt.Errorf("no helper version stated: %q", line)
	case info.Model == "":
		err = fmt.Errorf("no model stated: %q", line)
	case !sha256Re.MatchString(info.ModelSHA256):
		err = fmt.Errorf("model_sha256 is not a SHA-256 in hexadecimal: %q", line)
	}
	if err != nil {
		info = VersionInfo{}
	}
	return
}
