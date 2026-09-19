package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/sptenc/core"

	"github.com/hekmon/cunits/v3"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/urfave/cli/v3"
)

// cacheEntry holds parsed metadata from a single cache file.
type cacheEntry struct {
	identity core.CacheFileIdentity
	filename string
	runs     int
	segments int
	meanQP   float64
	size     int64
	modified time.Time
}

var cacheCommand = &cli.Command{
	Name:     "cache",
	Category: "Tooling",
	Usage:    "Manage the persistent QP search statistics cache",
	Description: "Inspect and clear the QP history cache files that speed up future encodes.\n\n" +
		"Each cache file is named after the encoder, VMAF profile, and optional cache profile that\n" +
		"produced it. The cache command decodes these filenames so you can see exactly which\n" +
		"thresholds and content types each entry represents without guessing from opaque hashes.",
	Commands: []*cli.Command{
		cacheListCommand,
		cacheClearCommand,
	},
}

var cacheListCommand = &cli.Command{
	Name:    "list",
	Aliases: []string{"l"},
	Usage:   "List all cached QP history entries",
	Description: "Scan the cache directory, decode each filename to show the encoder, VMAF profile,\n" +
		"and optional cache profile, then read the JSON contents to display run count, total segments,\n" +
		"and mean QP. Entries are sorted by encoder then modification time (newest first).",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     statsCacheDirFlagName,
			Aliases:  []string{"s"},
			Usage:    "Directory where QP statistics are stored",
			Value:    getCacheDir(),
			OnlyOnce: true,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		entries, err := loadCacheEntries(cmd.String(statsCacheDirFlagName))
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("No cache entries found.")
			return nil
		}

		// Sort by encoder, then newest first
		sortCacheEntries(entries)

		// Build table
		var buf strings.Builder
		t := tablewriter.NewTable(&buf,
			tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{
				Borders: tw.BorderNone,
				Settings: tw.Settings{
					Separators: tw.Separators{BetweenColumns: tw.On},
				},
			})),
			tablewriter.WithConfig(tablewriter.Config{
				Header: tw.CellConfig{
					Formatting: tw.CellFormatting{AutoFormat: tw.Off},
				},
			}),
		)
		t.Header("Encoder", "Profile", "VMAF Thresholds", "Runs", "Segments", "Mean QP", "Size", "Modified")

		for _, e := range entries {
			profile := e.identity.CacheProfile
			if profile == "" {
				profile = "-"
			}
			thresholdStr := formatThresholdsCompact(e.identity.Profile)
			meanQPStr := "-"
			if e.runs > 0 {
				meanQPStr = strconv.FormatFloat(e.meanQP, 'f', 1, 64)
			}
			t.Append([]string{
				e.identity.Encoder,
				profile,
				thresholdStr,
				strconv.Itoa(e.runs),
				strconv.Itoa(e.segments),
				meanQPStr,
				fmt.Sprint(cunits.ImportInBytes(float64(e.size))),
				humanTime(e.modified),
			})
		}
		t.Render()
		fmt.Println(buf.String())
		return nil
	},
}

var cacheClearCommand = &cli.Command{
	Name:    "clear",
	Aliases: []string{"c"},
	Usage:   "Clear cache entries matching a pattern (or all if no pattern is given)",
	Description: "Delete cached QP history files.\n\n" +
		"Without arguments, deletes every cache file in the cache directory.\n" +
		"With a pattern, deletes only entries whose encoder name, cache profile, or\n" +
		"VMAF threshold string contains the pattern (case-insensitive).\n\n" +
		"This is a destructive operation. You must pass --yes to confirm.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     statsCacheDirFlagName,
			Aliases:  []string{"s"},
			Usage:    "Directory where QP statistics are stored",
			Value:    getCacheDir(),
			OnlyOnce: true,
		},
		&cli.BoolFlag{
			Name:     "yes",
			Aliases:  []string{"y"},
			Usage:    "Confirm deletion without interactive prompt",
			Value:    false,
			OnlyOnce: true,
		},
	},
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "pattern",
			UsageText: "[pattern]",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		cacheDir := cmd.String(statsCacheDirFlagName)
		pattern := strings.ToLower(cmd.StringArg("pattern"))

		entries, err := loadCacheEntries(cacheDir)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("No cache entries to clear.")
			return nil
		}

		var toDelete []*cacheEntry
		if pattern == "" {
			for i := range entries {
				toDelete = append(toDelete, &entries[i])
			}
		} else {
			for i := range entries {
				e := &entries[i]
				if strings.Contains(strings.ToLower(e.identity.Encoder), pattern) ||
					strings.Contains(strings.ToLower(e.identity.CacheProfile), pattern) ||
					strings.Contains(strings.ToLower(formatThresholdsCompact(e.identity.Profile)), pattern) {
					toDelete = append(toDelete, e)
				}
			}
		}

		if len(toDelete) == 0 {
			fmt.Println("No cache entries matched the pattern.")
			return nil
		}

		// Preview what will be deleted
		fmt.Printf("The following %d cache file(s) will be deleted:\n\n", len(toDelete))
		for _, e := range toDelete {
			fmt.Printf("  %s\n", filepath.Base(e.filename))
		}
		fmt.Println()

		if !cmd.Bool("yes") {
			return fmt.Errorf("deletion aborted; pass --yes (or -y) to confirm")
		}

		for _, e := range toDelete {
			if err := os.Remove(e.filename); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to delete %s: %v\n", filepath.Base(e.filename), err)
			} else {
				fmt.Printf("Deleted %s\n", filepath.Base(e.filename))
			}
		}
		return nil
	},
}

// loadCacheEntries scans the cache directory and parses every qphistory_*.json file.
func loadCacheEntries(dir string) ([]cacheEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read cache directory: %w", err)
	}

	var result []cacheEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, core.CacheFilePrefix) || !strings.HasSuffix(name, core.CacheFileExtension) {
			continue
		}

		identity, ok := core.ParseCacheFilename(name)
		if !ok {
			continue
		}
		fullPath := filepath.Join(dir, name)

		info, err := entry.Info()
		if err != nil {
			continue
		}

		e := cacheEntry{
			identity: identity,
			filename: fullPath,
			size:     info.Size(),
			modified: info.ModTime(),
		}

		// Read JSON contents
		_ = readCacheStats(fullPath, &e)

		result = append(result, e)
	}
	return result, nil
}

// readCacheStats populates runs, segments and meanQP from the JSON file.
func readCacheStats(path string, e *cacheEntry) error {
	runs, err := core.LoadRunStats(path)
	if err != nil {
		return err
	}

	e.runs = len(runs)
	var totalWeight int
	var weightedMean float64
	for _, r := range runs {
		totalWeight += r.Weight
		weightedMean += r.Mean * float64(r.Weight)
	}
	e.segments = totalWeight
	if totalWeight > 0 {
		e.meanQP = weightedMean / float64(totalWeight)
	}
	return nil
}

// formatThresholdsCompact renders active thresholds as a comma-separated list.
func formatThresholdsCompact(vc core.VMAFChecker) string {
	m := vc.Thresholds()
	if len(m) == 0 {
		return "-"
	}
	order := []string{"min", "p1", "p5", "p10", "p25", "median", "mean", "hmean"}
	var parts []string
	for _, key := range order {
		if v, ok := m[key]; ok {
			parts = append(parts, fmt.Sprintf("%s=%s", key, strconv.FormatFloat(v, 'f', -1, 64)))
		}
	}
	return strings.Join(parts, ", ")
}

func sortCacheEntries(entries []cacheEntry) {
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].identity.Encoder > entries[j].identity.Encoder ||
				(entries[i].identity.Encoder == entries[j].identity.Encoder && entries[i].modified.Before(entries[j].modified)) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}

func humanTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
