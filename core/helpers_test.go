package core

import (
	"strings"
	"testing"
)

func TestGetFileSize_Error(t *testing.T) {
	_, err := getFileSize("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}
	if !strings.Contains(err.Error(), "failed to stat path") {
		t.Errorf("expected 'failed to stat path' in error, got %v", err)
	}
}
