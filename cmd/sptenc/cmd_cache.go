package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	index    int // what the user designates the entry by, see loadCacheEntries
	identity core.CacheFileIdentity
	filename string
	runs     int
	segments int
	meanQP   float64
	stddevQP float64
	size     int64
	modified time.Time
	readErr  error // the file could not be read: the stats above are empty
}

var cacheCommand = &cli.Command{
	Name:     "cache",
	Category: "Tooling",
	Usage:    "Manage the persistent QP search statistics cache",
	Description: "Inspect and delete the QP history cache files that speed up future encodes.\n\n" +
		"Each cache file is named after the encoder, VMAF profile, and optional cache profile that\n" +
		"produced it. The cache command decodes these filenames so you can see exactly which\n" +
		"thresholds and content types each entry represents without guessing from opaque hashes.\n" +
		"Entries are designated by the index 'list' shows in its first column.",
	Commands: []*cli.Command{
		cacheListCommand,
		cacheDeleteCommand,
	},
}

var cacheListCommand = &cli.Command{
	Name:    "list",
	Aliases: []string{"l"},
	Usage:   "List all cached QP history entries",
	Description: "Scan the cache directory, decode each filename to show the encoder, VMAF profile,\n" +
		"and optional cache profile, then read the JSON contents to display run count, total segments,\n" +
		"and mean QP.\n\n" +
		"The first column is the index 'sptenc cache delete' takes. Entries are sorted by encoder, VMAF\n" +
		"model, cache profile, then VMAF thresholds: an index only changes when a cache file is created\n" +
		"or deleted, not when an encode adds a run to one.\n\n" +
		"Cache files that can not be read are listed below the table with their own index: an encode\n" +
		"needing one of them stops until it is deleted.",
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
		printCacheEntries(entries)
		return nil
	},
}

var cacheDeleteCommand = &cli.Command{
	Name:    "delete",
	Aliases: []string{"d"},
	Usage:   "Delete cache entries by their index in 'cache list' (all of them if no index is given)",
	Description: "Delete cached QP history entries, designated by the index shown in the first column of\n" +
		"'sptenc cache list'. Without an index, every entry is deleted.\n\n" +
		"Give all the indexes to delete in one call: once an entry is deleted, the index of every\n" +
		"entry after it drops by one, and the listing they were read from no longer matches.\n\n" +
		"The entries are shown and a confirmation is asked before anything is deleted. --yes skips the\n" +
		"question, for scripts: the indexes are then trusted as they are.",
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
		&cli.StringArgs{
			Name:      "index",
			Min:       0,
			Max:       -1,
			UsageText: "[index ...]",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		entries, err := loadCacheEntries(cmd.String(statsCacheDirFlagName))
		if err != nil {
			return err
		}
		indexes := cmd.StringArgs("index")
		toDelete, err := selectCacheEntries(entries, indexes)
		if err != nil {
			return err
		}
		if len(toDelete) == 0 {
			fmt.Println("No cache entries to delete.")
			return nil
		}

		// The indexes are resolved once, here: what is shown is what gets deleted, even if the
		// cache directory changes while the question waits for an answer.
		printCacheEntries(toDelete)
		if !cmd.Bool("yes") {
			var question string
			switch {
			case len(toDelete) == 1:
				question = "Delete this cache entry?"
			case len(indexes) == 0:
				question = fmt.Sprintf("Delete all %d cache entries?", len(toDelete))
			default:
				question = fmt.Sprintf("Delete these %d cache entries?", len(toDelete))
			}
			confirmed, err := askConfirmation(ctx, question)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Println("Nothing deleted.")
				return nil
			}
		}

		var failed int
		for _, e := range toDelete {
			if err := core.RemoveCacheFile(e.filename); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to delete #%d: %s\n", e.index, errorWithoutPath(err))
				failed++
			}
		}
		if deleted := len(toDelete) - failed; deleted > 0 {
			fmt.Printf("Deleted %d cache %s.\n", deleted, pluralize(deleted, "entry", "entries"))
		}
		if failed > 0 {
			return fmt.Errorf("failed to delete %d of %d cache entries", failed, len(toDelete))
		}
		return nil
	},
}

// loadCacheEntries scans the cache directory, parses every qphistory_*.json file and returns them
// sorted and indexed. The index is how the user designates an entry: every command must get its
// entries from here, so that delete resolves an index to the entry list showed with it.
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
		// Read JSON contents. A file that can not be read is kept: it still has to be listed to be
		// deletable, and it is the one to delete as an encode stops on it.
		if e.readErr = readCacheStats(fullPath, &e); errors.Is(e.readErr, fs.ErrNotExist) {
			continue // deleted since the directory was read
		}
		result = append(result, e)
	}
	sortCacheEntries(result)
	for i := range result {
		result[i].index = i + 1
	}
	return result, nil
}

// readCacheStats populates runs, segments, meanQP and stddevQP from the JSON file.
func readCacheStats(path string, e *cacheEntry) error {
	runs, err := core.LoadRunStats(path)
	if err != nil {
		return err
	}
	e.runs = len(runs)
	var (
		totalWeight    int
		weightedMean   float64
		weightedStdDev float64
	)
	for _, r := range runs {
		totalWeight += r.Weight
		weightedMean += r.Mean * float64(r.Weight)
		weightedStdDev += r.StdDev * float64(r.Weight)
	}
	e.segments = totalWeight
	if totalWeight > 0 {
		e.meanQP = weightedMean / float64(totalWeight)
		e.stddevQP = weightedStdDev / float64(totalWeight)
	}
	return nil
}

// sortCacheEntries orders the entries by what they hold, never by when they were written: an
// encode adding a run to a cache file between a list and a delete must not move the other
// entries' indexes. The unreadable ones come last, as they are printed below the table. The
// filename settles what is left (two names decoding to the same identity, only possible by hand).
func sortCacheEntries(entries []cacheEntry) {
	slices.SortFunc(entries, func(a, b cacheEntry) int {
		if (a.readErr == nil) != (b.readErr == nil) {
			if a.readErr == nil {
				return -1
			}
			return 1
		}
		return cmp.Or(
			strings.Compare(a.identity.Encoder, b.identity.Encoder),
			strings.Compare(a.identity.VMAFModel, b.identity.VMAFModel),
			strings.Compare(a.identity.CacheProfile, b.identity.CacheProfile),
			compareThresholds(a.identity.Profile, b.identity.Profile),
			strings.Compare(a.filename, b.filename),
		)
	})
}

// thresholdsOrder is the order VMAF thresholds are displayed and sorted in.
var thresholdsOrder = []string{"min", "p1", "p5", "p10", "p25", "median", "mean", "hmean"}

// compareThresholds compares two VMAF profiles threshold by threshold, an inactive threshold
// coming before any value.
func compareThresholds(a, b core.VMAFChecker) int {
	am, bm := a.Thresholds(), b.Thresholds()
	for _, key := range thresholdsOrder {
		av, aOK := am[key]
		if !aOK {
			av = core.VMAFOffValue
		}
		bv, bOK := bm[key]
		if !bOK {
			bv = core.VMAFOffValue
		}
		if c := cmp.Compare(av, bv); c != 0 {
			return c
		}
	}
	return 0
}

// selectCacheEntries resolves the indexes given on the command line to their entries, in listing
// order and each once, or returns every entry when no index is given. All the indexes are checked
// before anything is returned: an invalid one must not leave the valid ones deleted.
func selectCacheEntries(entries []cacheEntry, indexes []string) ([]cacheEntry, error) {
	if len(indexes) == 0 {
		return entries, nil
	}
	selected := make([]bool, len(entries))
	for _, arg := range indexes {
		index, err := strconv.Atoi(arg)
		if err != nil {
			return nil, fmt.Errorf("invalid cache index %q: expected the number in the first column of 'sptenc cache list'", arg)
		}
		if index < 1 || index > len(entries) {
			if len(entries) == 0 {
				return nil, fmt.Errorf("no cache entry #%d: the cache is empty", index)
			}
			return nil, fmt.Errorf("no cache entry #%d: indexes go from 1 to %d, see 'sptenc cache list'", index, len(entries))
		}
		selected[index-1] = true
	}
	var result []cacheEntry
	for i, e := range entries {
		if selected[i] {
			result = append(result, e)
		}
	}
	return result, nil
}

// printCacheEntries prints the readable entries as a table and the unreadable ones below it, each
// with its index.
func printCacheEntries(entries []cacheEntry) {
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
	t.Header("#", "Encoder", "Model", "Profile", "VMAF Thresholds", "Runs", "Segments", "Mean QP", "Stddev QP", "Size", "Modified")
	var (
		rows       int
		unreadable []cacheEntry
	)
	for _, e := range entries {
		if e.readErr != nil {
			unreadable = append(unreadable, e)
			continue
		}
		profile := e.identity.CacheProfile
		if profile == "" {
			profile = "-"
		}
		model := e.identity.VMAFModel
		if model == "" {
			model = "-"
		}
		thresholdStr := formatThresholdsCompact(e.identity.Profile)
		meanQPStr := "-"
		stddevQPStr := "-"
		if e.runs > 0 {
			meanQPStr = strconv.FormatFloat(e.meanQP, 'f', 1, 64)
			stddevQPStr = strconv.FormatFloat(e.stddevQP, 'f', 1, 64)
		}
		t.Append([]string{
			strconv.Itoa(e.index),
			e.identity.Encoder,
			model,
			profile,
			thresholdStr,
			strconv.Itoa(e.runs),
			strconv.Itoa(e.segments),
			meanQPStr,
			stddevQPStr,
			fmt.Sprint(cunits.ImportInBytes(float64(e.size))),
			humanTime(e.modified),
		})
		rows++
	}
	fmt.Println()
	if rows > 0 {
		t.Render()
		fmt.Println(buf.String())
	}
	if len(unreadable) > 0 {
		fmt.Println("Unreadable cache files (an encode needing one of them stops until it is deleted):")
		for _, e := range unreadable {
			fmt.Printf("  #%d %s: %s\n", e.index, describeCacheIdentity(e.identity), errorWithoutPath(e.readErr))
		}
		fmt.Println()
	}
}

// describeCacheIdentity renders what a cache file holds on one line, where a table row does not fit.
func describeCacheIdentity(identity core.CacheFileIdentity) string {
	parts := []string{identity.Encoder}
	if identity.VMAFModel != "" {
		parts = append(parts, identity.VMAFModel)
	}
	if identity.CacheProfile != "" {
		parts = append(parts, "profile "+strconv.Quote(identity.CacheProfile))
	}
	parts = append(parts, formatThresholdsCompact(identity.Profile))
	return strings.Join(parts, " / ")
}

// errorWithoutPath returns the message of err without the path of the file it is about: the entry
// is already designated by its index and what it holds, its filename would add nothing readable.
func errorWithoutPath(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return strings.Replace(err.Error(), " "+pathErr.Path, "", 1)
	}
	return err.Error()
}

// askConfirmation asks a yes/no question on the terminal, no being the default. Having nothing to
// read the answer from (standard input empty or closed, as in a script) is an error: the
// confirmation was not given, --yes is how a script gives it.
func askConfirmation(ctx context.Context, question string) (bool, error) {
	fmt.Printf("%s [y/N] ", question)
	type answer struct {
		line string
		err  error
	}
	answers := make(chan answer, 1)
	// The read can not be interrupted: on Ctrl+C the question is abandoned and this goroutine stays
	// blocked until the process exits, right after.
	go func() {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		answers <- answer{line: line, err: err}
	}()
	var a answer
	select {
	case <-ctx.Done():
		fmt.Println()
		return false, fmt.Errorf("deletion aborted: %w", ctx.Err())
	case a = <-answers:
	}
	if a.err != nil && (!errors.Is(a.err, io.EOF) || a.line == "") {
		fmt.Println()
		if !errors.Is(a.err, io.EOF) {
			return false, fmt.Errorf("failed to read the answer: %w", a.err)
		}
		// Ctrl+C on a Windows console ends the read with nothing read, which Go reports as io.EOF,
		// and the interruption only reaches ctx afterwards: give it a moment before blaming the
		// standard input.
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("deletion aborted: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
			return false, errors.New("deletion aborted: no answer on the standard input, pass --yes (or -y) to confirm without the question")
		}
	}
	switch strings.ToLower(strings.TrimSpace(a.line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// formatThresholdsCompact renders active thresholds as a comma-separated list.
func formatThresholdsCompact(vc core.VMAFChecker) string {
	m := vc.Thresholds()
	if len(m) == 0 {
		return "-"
	}
	var parts []string
	for _, key := range thresholdsOrder {
		if v, ok := m[key]; ok {
			parts = append(parts, fmt.Sprintf("%s=%s", key, strconv.FormatFloat(v, 'f', -1, 64)))
		}
	}
	return strings.Join(parts, ", ")
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
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
