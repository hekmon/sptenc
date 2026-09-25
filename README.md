# Split Encoder

`sptenc` (Split Encoder) is a scene-aware, [VMAF](https://github.com/Netflix/vmaf)-driven video transcoder for enthusiasts who want every scene of their encodes to meet a measured quality target, at the smallest size that still meets it, and accept to pay for that in encoding time with a CPU encoder, or in file size with a GPU one.

It splits the input into scene-aligned segments, encodes each one independently, and validates the result against configurable VMAF thresholds before accepting it.
Failed segments are automatically re-encoded at a lower QP until all thresholds are met.
A final, complete VMAF comparison between the encoded output and original source is performed at the end, and its results are embedded into the output file's metadata tags.

This approach gives each scene the highest QP, so the smallest size, that still passes the quality target defined by the VMAF profile.

> **Trade-off:** several QP values are tested on every segment and each one is measured with VMAF, so an encode costs a few passes of the chosen encoder where a CRF encode costs one. What that costs depends on the encoder: a larger file with a GPU encoder, which searches faster than realtime once several segments run in parallel, hours and the smallest file with a CPU one (measured in [Encoder selection vs file size](MANUAL.md#encoder-selection-vs-file-size)). The floor is the same either way, and it is not what a single CRF pass with a whole-file VMAF check gives: that only validates an average, so one complex scene in an otherwise steady movie can be destroyed while the overall score still looks fine. sptenc enforces its quality floor on every single scene independently.

> **Inspiration:** sptenc is inspired by Netflix's [Dynamic Optimizer](https://netflixtechblog.com/dynamic-optimizer-a-perceptual-video-encoding-optimization-framework-e19f1e3a277f) framework, which pioneered scene-aware, perceptually-optimized video encoding, and the VMAF perceptual quality models that power it. This project is built for power users encoding on their own hardware, not for streaming-scale infrastructure.

## Who is this for?

You probably don't need sptenc if you just want to shrink a video for your phone. Standard tools like HandBrake or ffmpeg with CRF are faster and perfectly fine for that.

sptenc is built for workflows where you want the **smallest file size that still meets a VMAF floor you can prove**:

- **Archival & preservation** — You have a high-bitrate source or lossless master (or an expensive AI-upscaled restoration) and want to compress it without ever dropping below a VMAF floor you can prove.
- **Quality-per-bit optimization** — You target a specific visual fidelity at the smallest size and currently do manual CRF sweeps, screenshot comparisons, or test encodes to find the right settings. sptenc automates that search and produces a VMAF report documenting the result.
- **Large collection processing** — You process many files against a single, carefully tuned quality profile. sptenc treats that profile as a contract: every segment is encoded, measured, and corrected until it passes, without you checking scores by hand.
- **NAS / media server optimization** — You maintain a personal library of high-bitrate remuxes and need to balance quality against storage costs. sptenc replaces manual CRF trial-and-error with a measurable guarantee, so you keep the quality that matters and reclaim the space that doesn't.

If you already know why CRF averages can hide bad frames, sptenc closes the loop: encode, measure, correct, converge.

Coming from Av1an or CRF with VMAF spot checks? See [how sptenc compares](MANUAL.md#compared-to-other-approaches).

## Key Features

- 🎯 **VMAF-driven encoding** - The quality gate is a perceptual score measured on the output, not a CRF or bitrate you hope will be enough. 1080p or 4K model auto-selected from the input, NEG variants available (see [what VMAF does not see](MANUAL.md#what-vmaf-does-not-see))
- 🎬 **Scene-aware segmentation** - Segments are cut on scene changes, so the quality floor is enforced per scene, never averaged across a whole file
- 📊 **Multi-metric validation** - Combine mean, harmonic mean, median, percentiles (P1/P5/P10/P25) and worst frame; every enabled threshold must pass
- 🧠 **Adaptive QP search** - Each segment converges on the highest QP that still passes in a few attempts, and stats kept from previous runs make the next ones start closer (see [Adaptive QP Search](MANUAL.md#adaptive-qp-search))
- ⚡ **CPU and GPU encoders** - HEVC and AV1 with `libx265`, `libsvtav1`, NVENC, VAAPI, D3D12VA and VideoToolbox. Prototype a VMAF profile fast on the GPU and encode the final file small on the CPU, or keep the GPU encode when time matters more than size: the floor is proven the same way (see [Encoders](MANUAL.md#encoders))
- 🚀 **Hardware acceleration wherever it helps** - CUDA VMAF, GPU decoding even alongside a CPU encoder, and several segments searched in parallel: same output, less time
- 🔬 **Automatic scene threshold search** - `batchsearch` tries several scene detection thresholds and keeps the one that produces the smallest passing file
- 📋 **A file you can trust** - Audio, subtitles and color metadata are carried over (PCM audio losslessly compressed to FLAC), and the final whole-file VMAF result is written into the output's metadata tags

## How It Works

1. **Scene detection** - FFmpeg `scdet` analyzes the video to find scene boundaries, producing semantically coherent segments. For precise frame-accurate cuts, a lossless FFV1 master is used: every frame is self-contained, so splits can happen at any frame without quality loss. It also shields against open GOPs, where a cut can leave frames referencing others in the neighboring segment: they can not be decoded anymore and are dropped, a drift (against the audio for instance) that accumulates with every cut.
2. **Per-segment encoding** - Each segment is encoded independently with the chosen encoder (e.g. `libx265`, `hevc_nvenc`, `libsvtav1`).
3. **VMAF validation (post-encode)** - After encoding, each segment's VMAF scores are computed and checked against all configured thresholds. Any failure triggers a re-encode at a lower QP.
4. **Adaptive QP search** - Each segment starts from a smart QP estimate, brackets the valid range with stepped probes, then uses interpolation to converge on the highest valid QP (smallest file) in just a few attempts. Persistent stats from previous runs further accelerate this (see [Adaptive QP Search](MANUAL.md#adaptive-qp-search)).
5. **Best effort** - If the encoder's minimum QP is reached and thresholds are still not met (e.g. pathological scene), the segment is accepted and flagged as "best effort" in logs.
6. **Muxing & tagging** - Segments are merged into a single output file. Audio, subtitles, and other streams from the original source are remuxed into the final file. PCM audio tracks are automatically losslessly compressed to FLAC. A final VMAF comparison between the complete encoded file and original source is performed, with results displayed in logs and embedded in the output file's metadata tags. Matroska statistics tags are regenerated for full player compatibility.

## Commands

sptenc is organized into subcommands. Run `sptenc <command> --help` for detailed usage of each.

| Command | Alias | Category | Purpose |
|---|---|---|---|
| `encode` | `e` | Main | Full pipeline: split (if needed), encode segments, validate VMAF, remux, and tag |
| `check` | `k` | Main | Check that ffmpeg, ffprobe, and mkvpropedit are present and list available encoders |
| `master` | `m` | Tooling | Create a lossless FFV1 intermediate from a source file for frame-accurate splitting |
| `split` | `s` | Tooling | Detect scene changes and split a video into separate segment files |
| `concat` | `c` | Tooling | Concatenate video files from a directory into a single file without re-encoding |
| `remux` | `r` | Tooling | Replace the video track of a file with a new one without re-encoding |
| `vmaf` | `v` | Tooling | Compute VMAF between a reference and a distorted video |
| `thresholds` | `t` | Tooling | Preview candidate thresholds and their scene distributions without encoding |
| `cache` | — | Tooling | List the persistent QP statistics cache entries and delete them by index |
| `batchsearch` | `bs` | Advanced | Search for the scene threshold that yields the smallest passing file by encoding multiple candidates |

## Quick Start

### Check your environment
```bash
./sptenc check
```

### Basic encode - VMAF harmonic mean ≥ 93 (default)
```bash
./sptenc encode video.mkv output.mkv
```

### Strict quality with multiple thresholds
```bash
./sptenc encode video.mkv output.mkv --vmaf-mean 95 --vmaf-p5 85 --vmaf-min 70
```

### Use the VMAF NEG models
```bash
./sptenc encode video.mkv output.mkv --vmaf-neg
```

### Fast VMAF profile prototyping with NVENC on the second GPU
```bash
./sptenc encode video.mkv output.mkv --encoder hevc_nvenc --vmaf-cuda --nvidia-gpu-index 1 --vmaf-mean 93
# Once happy with the profile, re-run with the default libx265 for a smaller file, or keep this one
```

### CPU encode with the GPU decoding on the side
```bash
# libx265 runs on the CPU: scene detection, the master, the frame counts and the distorted side
# of VMAF are decoded by NVDEC instead (a hardware encoder does this by itself)
./sptenc encode video.mkv output.mkv --nvdec --vmaf-mean 93
```

### Pre-segmented directory
```bash
./sptenc encode ./gop_dir/ output.mkv --original-file original_with_audio.mkv --vmaf-mean 95
```

### Concatenate segments without re-encoding
```bash
./sptenc concat ./segments/ merged.mkv
```

### Compute VMAF between two videos
```bash
# Software libvmaf with GPU-accelerated decoding
./sptenc vmaf --nvdec original.mkv encoded.mkv

# Full GPU acceleration (requires libvmaf_cuda)
./sptenc vmaf --vmaf-cuda --vmaf-neg original.mkv encoded.mkv
```

### Search for the threshold that yields the smallest passing file
```bash
# Fast search on GPU: the output is the best candidate, encoded by the GPU encoder
./sptenc batchsearch video.mkv output.mkv --encoder hevc_nvenc --vmaf-cuda --vmaf-hmean 93
# Same search, but once the best threshold is found the file is re-encoded with the equivalent
# CPU encoder to shrink it even further (one run: no need to run the search first)
./sptenc batchsearch video.mkv output.mkv --encoder hevc_nvenc --vmaf-cuda --vmaf-hmean 93 --final-encode
```

### Manual pipeline (master → split → encode)
```bash
./sptenc master video.mkv master.mkv
./sptenc split master.mkv ./segments/ --master --min-threshold 12
./sptenc encode ./segments/ output.mkv --original-file video.mkv
```

> Use the `thresholds` command to preview candidate thresholds and their scene distributions without encoding. Experiment with `--min-threshold` (0–100): higher values detect fewer scenes, lower values detect more. `split` and `encode` default to 10, ffmpeg's own; `thresholds` and `batchsearch` search upward from 8.
>
> **Use everything you have.** A hardware encoder decodes with its own GPU and `--vmaf-cuda` decodes with NVDEC: nothing to set. With a CPU encoder (the smallest files, out of the CPU alone), hand the decoding to whatever GPU is in the machine, integrated or Apple silicon included: `--nvdec`, `--vaapi-dec`, `--d3d12va-dec` or `--videotoolbox-dec`. Same file out, more CPU left for the encoder. The same flags serve `master`, `split`, `thresholds` and `vmaf`. You can not get it wrong: an unsupported source codec falls back to software with a warning, a flag contradicting the encoder is refused. Only the lossless FFV1 intermediate has no hardware decoder.

## Installation

Standalone binaries for most platforms are available on the [releases page](https://github.com/hekmon/sptenc/releases).

**External Dependencies:**
- `ffmpeg` - the one your distribution, Homebrew or the [static builds](https://ffmpeg.org/download.html) ship is enough: it must be built with `libvmaf` and with `libx265` (or another supported encoder), which the usual packages are. A recent version is highly recommended. Nothing to compile, with one exception: `--vmaf-cuda` needs `libvmaf_cuda`, which no package ships. See the [build guide](https://gist.github.com/hekmon/b273e55139183370c5000f766fccc128) for that one (works in WSL on Windows).
- `ffprobe` - bundled with ffmpeg
- `mkvpropedit` - from [MKVToolNix](https://mkvtoolnix.download/)

Run `sptenc check` to verify everything is found and usable. On Windows, `ffmpeg.exe` and `ffprobe.exe` are looked for in the current directory, not in `PATH`: see [where the binaries are looked for](MANUAL.md#installation-details).

## Going further

Still here? The [manual](MANUAL.md) covers everything else: VMAF models and thresholds, encoder choice and file size, scene detection, the QP search and its cache, the output and its tags, and how sptenc compares to other approaches.

## License

MIT. See [LICENSE](LICENSE).
