package ffmpeg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateConcatList(t *testing.T) {
	tmpDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get the current directory: %v", err)
	}
	absFile := filepath.Join(tmpDir, `we'ird`, "seg_000001.mkv")

	listPath, err := GenerateConcatList(tmpDir, []string{
		filepath.Join("relative", "seg_000000.mkv"),
		absFile,
	})
	if err != nil {
		t.Fatalf("GenerateConcatList failed: %v", err)
	}
	content, err := os.ReadFile(string(listPath))
	if err != nil {
		t.Fatalf("failed to read the generated list: %v", err)
	}

	expected := concatScriptHeader + "\n" +
		// relative paths must be made absolute: ffmpeg resolves them against the list directory
		"file '" + filepath.Join(cwd, "relative", "seg_000000.mkv") + "'\n" +
		// quotes must be escaped the ffmpeg way
		"file '" + strings.ReplaceAll(absFile, `'`, `'\''`) + "'\n"
	if string(content) != expected {
		t.Errorf("unexpected concat list:\n%s\nexpected:\n%s", content, expected)
	}
}

func TestGenerateConcatList_NoFiles(t *testing.T) {
	if _, err := GenerateConcatList(t.TempDir(), nil); err == nil {
		t.Error("expected an error with no files")
	}
}
