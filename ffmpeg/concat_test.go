package ffmpeg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	}, nil)
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
	if _, err := GenerateConcatList(t.TempDir(), nil, nil); err == nil {
		t.Error("expected an error with no files")
	}
}

func TestGenerateConcatList_Durations(t *testing.T) {
	tmpDir := t.TempDir()
	files := []string{
		filepath.Join(tmpDir, "seg_000000.mkv"),
		filepath.Join(tmpDir, "seg_000001.mkv"),
	}
	// 60 and 886 frames at 24000/1001 fps, rounded to the microsecond
	listPath, err := GenerateConcatList(tmpDir, files, []time.Duration{
		2502500 * time.Microsecond,
		36953583 * time.Microsecond,
	})
	if err != nil {
		t.Fatalf("GenerateConcatList failed: %v", err)
	}
	content, err := os.ReadFile(string(listPath))
	if err != nil {
		t.Fatalf("failed to read the generated list: %v", err)
	}
	expected := concatScriptHeader + "\n" +
		"file '" + files[0] + "'\n" +
		"duration 2.502500\n" +
		"file '" + files[1] + "'\n" +
		"duration 36.953583\n"
	if string(content) != expected {
		t.Errorf("unexpected concat list:\n%s\nexpected:\n%s", content, expected)
	}
}

func TestGenerateConcatList_DurationsMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	files := []string{filepath.Join(tmpDir, "a.mkv"), filepath.Join(tmpDir, "b.mkv")}
	if _, err := GenerateConcatList(tmpDir, files, []time.Duration{time.Second}); err == nil {
		t.Error("expected an error with fewer durations than files")
	}
	if _, err := GenerateConcatList(tmpDir, files, []time.Duration{time.Second, -time.Second}); err == nil {
		t.Error("expected an error with a negative duration")
	}
}
