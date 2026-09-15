package mkvtoolnix

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// GetMKVPropEditVersion runs mkvpropedit --version and parses its output.
func GetMKVPropEditVersion(ctx context.Context) (version string, err error) {
	args := []string{"--version"}
	cmd := exec.CommandContext(ctx, MKVPropEdit, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s %s", MKVPropEdit, err, MKVPropEdit, strings.Join(args, " "))
		return
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s %s", MKVPropEdit, err, stderr.String(), MKVPropEdit, strings.Join(args, " "))
		return
	}
	version, err = parseMKVPropEditVersion(stdout.String())
	if err != nil {
		err = fmt.Errorf("error parsing %s output: %w", MKVPropEdit, err)
	}
	return
}

var mkvPropEditVersionRe = regexp.MustCompile(`^mkvpropedit (.+)$`)

func parseMKVPropEditVersion(output string) (version string, err error) {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := mkvPropEditVersionRe.FindStringSubmatch(line); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("version line not found in output")
}
