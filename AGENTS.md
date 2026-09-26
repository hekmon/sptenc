# sptenc Context for Contributors and Coding Agents

**Read this before modifying any code.** It will save time and prevent broken assumptions.

## What this is

`sptenc` is a **perceptual-quality, VMAF-driven video encoder** written in Go. It targets a specific niche: **archival/preservation workflows where provable VMAF floors matter more than encoding speed**.

It is not a general-purpose ffmpeg wrapper, a bulk streaming prep tool, or a speed-oriented chunk encoder like Av1an. The tradeoffs are steep and intentional.

## The core concept

1. Detect scene boundaries and split the input into segments
2. For each segment, search empirically for the highest QP (smallest file) that passes all enabled VMAF quality thresholds
3. Re-encode at lower QP if any threshold fails
4. Concatenate validated segments and embed VMAF metadata into the output

This is a **closed-loop control system**, not a script that runs ffmpeg in a loop. Every design decision flows from this concept.

## Code orientation

| Package | What lives here | What to change carefully |
|---|---|---|
| `core/` | QP search algorithm, interpolation, cache, VMAF threshold checking, scene threshold candidate generation | Algorithmic changes. **Fully decoupled** from `ffmpeg` — do not re-introduce concrete `ffmpeg` imports here. |
| `ffmpeg/` | ffmpeg command builders, ffprobe parsers, encoder wrappers, VMAF computation, hardware detection | Platform-specific ffmpeg logic, new encoder support, hardware acceleration paths. |
| `pipeline/` | `EncoderAdapter` — bridges `core.SegmentEncoder` to concrete `ffmpeg` encoder functions, choosing each encoder's function, preset and device. `scenes.go` converts scenes between `ffmpeg` and `core`, and turns scene scores into reusable thresholds | A new encoder or a preset change here must follow in `ffmpeg/` (its encoder function, `GetEncoderQPRange`, `GetCPURelative`) and in `metadata/tags.go`, whose preset tag must list the very presets the adapter uses. |
| `metadata/` | `GenerateTags` — assembles ffmpeg metadata flags from `core` and `ffmpeg` results | Shared between CLI and any future front-ends (e.g. GUI). |
| `cmd/sptenc/` | urfave/cli v3 commands, orchestration, live progress UI | UX changes, new commands, workflow modifications. |
| `mkvtoolnix/` | `mkvpropedit` wrapper: version check and regeneration of the Matroska track statistics tags | The sptenc tags themselves are ffmpeg `-metadata` flags, built in `metadata/`. |

## Critical files to read before changing anything

- **`README.md`** — Overview, all commands, quick start.
- **`MANUAL.md`** — QP-vs-CRF rationale, Av1an comparison, VMAF thresholds, encoder choice, cache system, GPU selection.
- **`cmd/sptenc/cmd_batchsearch.go`** — The **parameter discovery engine**, not a utility. GPU-accelerated sweeps to find the optimal scene detection threshold before slow CPU final encodes. Includes the live progress UI, the pick of the smallest passing file and the `--strikes` early stop (the candidates themselves come from `searchCandidates` in `cmd/sptenc/helpers.go`, over `core.GetCandidates`).
- **`core/interfaces.go`** — `SegmentEncoder` interface contract; changes here affect both `core/` and `pipeline/`.
- **`core/qpsearch.go`** — Adaptive QP search algorithm. Statistical cache (mean/stddev) + Fritsch-Butland interpolation, converges in ~3–5 attempts per segment.
- **`core/predictor.go`** — Forecasts the VMAF record of an untested QP from the encoded ones, so the search picks its next encode inside the bracket instead of walking blind. A forecast is never accepted as a result. The 2024 benchmark in its comments is where Fritsch-Butland was chosen (the fewest attempts of four interpolators, on one clip); interpolating instead of bisecting the bracket only gains a little, depending on the content, and its attempt totals predate the ephemeral cache and are not the current cost.
- **`core/cache_persistent.go`** and **`core/cache_ephemeral.go`** — Persistent QP history with profile isolation, plus in-memory ephemeral stats for the current encode run.
- **`pipeline/encoder.go`** — The **adapter** that maps `core.SegmentEncoder` to the concrete ffmpeg encoder functions. Contains the explicit per-encoder switch choosing the function, preset and device of each encoder.
- **`ffmpeg/hevc.go` and `ffmpeg/av1.go`** — One function per encoder, spelling out every ffmpeg flag: `libx265`, `hevc_nvenc`, `hevc_vaapi`, `hevc_d3d12va` and `hevc_videotoolbox` in `hevc.go`; `libsvtav1`, `av1_nvenc`, `av1_vaapi` and `libaom-av1` in `av1.go`. `libaom-av1` exists in the adapter but is blocked from CLI selection as too slow for iterative QP search.
- **`ffmpeg/vmaf.go`** — VMAF computation with the v1 models (on the CPU, inputs decoded by the run's hardware decoder when their codec allows it), the libvmaf probe run at preflight, and the report parser (score and CAMBI picked out of templated feature keys).

## Key architectural constraints

### QP (Constant QP), not CRF

The search needs a dial that is deterministic and monotonic for a given segment: QP→VMAF is, which lets the interpolation converge in ~3–5 attempts per segment. CRF→VMAF is too (measured with libx265): "CRF is too noisy to be searched" was the documented reason for years and it is wrong, do not bring it back. One exception to the monotonicity is measured, with QP and the v1 models on a source that is banded already: the lowest QPs score below slightly higher ones (see the banded source point of "VMAF v1 by default, on the CPU").

The actual reasons:
- QP exists on every supported encoder, CRF does not (hardware encoders expose a constant quantizer or their own quality target). QP ranges, cache statistics and the GPU search → CPU final encode workflow are all built on that single dial.
- CRF is a rate control moving bits with the encoder's own perceptual model, which is not VMAF. Here the scene splitter and the per-segment search decide where quality varies, against the metric the user chose, and a segment cut on scene changes (short scenes merged into a neighbor) leaves little for a rate control to adapt to.

No claim is made about which one gives the smaller file at the same VMAF: it depends on the content. Switching the CPU encoders to CRF has been evaluated (file size needed to reach VMAF hmean 93 and 95, CRF relative to QP, three synthetic single-scene clips):
- `libx265`: from -7% to +49%. cu-tree helps, but adaptive quantization spends bits VMAF does not see, massively so on grain.
- `libsvtav1`: from -19% to 0%, never worse (ffmpeg's `-qp` sets SVT-AV1's `aq-mode` to 0, turning off its adaptive QP, while `-crf` keeps the default `aq-mode` 2, "deltaq pred efficiency").

Not obvious enough to change the dial every encoder shares, so QP stays. Reopening this needs measurements on real content, on several clips with different temporal structures: a single clip proves anything (a noisy one shows no difference at all, a static one shows a large gain).

**Implication:** Adding CRF modes or "hybrid CRF/QP" approaches is not adding a flag: it means another dial with its own range, its own statistics, and no equivalent on the hardware encoders the search workflow relies on.

### Segment encoding concurrency

Segments are encoded **sequentially by default**, but **optional concurrency** is supported via `QPSearchConfig.NbConcurrentSegments` (CLI: `--concurrent-segments` / `-C`), with every encoder. Results are the same whatever the value (checked down to the video stream checksum, again with VMAF v1: its CPU scores are identical whatever the number of threads and of computations running at once), only the time taken changes.

- **CPU encoders** (`libx265`, `libsvtav1`) used to be limited to 1 concurrent segment with a hard error, on the belief that a single encode saturates the CPU and that concurrent ones thrash cache and memory bandwidth. It was measured and it is wrong: x265 does create one thread per logical CPU, but a single encode does not keep a many-core CPU busy. On a 16 cores / 32 threads machine, the real pipeline with `libx265` at 1080p encoded 26% to 31% more frames per second with 2 and 3 concurrent segments (VMAF v0 on the CPU). Raw encodes: +45% to +60% at 1080p, +21% at 2160p with x265, +34% to +58% with SVT-AV1. Forcing the encoder to the physical cores count was measured too: same output, 10% slower, do not do it.
- **GPU encoders** benefit from concurrency well beyond the number of encoding engines on the card: a worker only feeds the encoder while it encodes and waits for VMAF the rest of the time, and the frames come from the CPU, which decodes the FFV1 intermediate twice per attempt (encode input, VMAF reference) and computes VMAF. Measured on an RTX 5090 (three NVENC engines) with a 16 cores / 32 threads CPU, `hevc_nvenc`, 26 min 1080p episode, 163 segments, cold cache: 17m10s with `-C 6` and VMAF v1 (CPU busy 81%, NVENC 24%). With VMAF v0 on CUDA (v0.1.0), when the CPU only decoded: 14m7s with `-C 3` (CPU 52%, NVENC 18%), 10m40s with `-C 6` (CPU 90%). The CPU is the ceiling, not the GPU; the driver's encode session limit is the hard stop. The `batchsearch` command encourages raising this value for GPU-based threshold discovery, followed by an optional CPU final encode (`--final-encode`) for maximum compression efficiency (same episode, `libx265` slow `-C 3`: 1h1m15s search with the CPU busy 90%, video stream 31% smaller than NVENC's). That final encode has its own concurrency flag (`--final-concurrent-segments`): the value chosen for a GPU says nothing about what the CPU can take.

Concurrency is implemented as a worker pool (`golang.org/x/sync/errgroup`) in `core/qpsearch.go`, with worker-scoped callbacks so the UI can attribute progress to individual workers.

**Implication:** The default remains sequential: the right value depends on the hardware (GPU session limits, CPU cores, memory) and is the user's to pick. Naive goroutine-per-segment approaches would fail on hardware limits. Segments searched together can not learn from each other through the ephemeral cache: the first wave of a search starts from the seed alone, a fixed cost paid while the seed is wrong. In `batchsearch` only the first candidate pays it: the command wraps the persistent cache in a `core.EphemeralStatsCache` for the whole run, every candidate search wraps that one and feeds it back its QPs, so the next candidates start from what the previous ones found on this very content (see Cache layering below). Only the winner reaches the persistent cache.

### Explicit per-encoder switches

`pipeline/encoder.go` contains a long, explicit `switch` that maps each encoder to its ffmpeg function, with its preset and device, and `ffmpeg/hevc.go` and `ffmpeg/av1.go` hold one function per encoder writing out every flag. This is intentional.

Each encoder uses **distinct ffmpeg semantics** (X is the QP being tested):
- `libx265`: `-preset slow -qp X` (no `aq-mode`: x265 disables AQ and cu-tree by itself in constant QP mode)
- `libsvtav1`: `-preset 3 -qp X` (ffmpeg's `-qp` sets `aq-mode` 0: no adaptive quantization)
- NVENC (`hevc_nvenc`, `av1_nvenc`): `-preset p7 -tune hq -rc constqp -qp X -rc-lookahead 32 -spatial-aq 1 -temporal-aq 1` (AQ is honored under `constqp`)
- `hevc_vaapi` and `hevc_d3d12va`: `-rc_mode CQP -qp X`
- `av1_vaapi`: `-rc_mode CQP -global_quality X` (ffmpeg's `av1_vaapi` has no `-qp`)
- `libaom-av1` (not selectable from the CLI): `-crf X -b:v 0`, aom's Q mode (`--end-usage=q`)
- VideoToolbox: `-q:v 101-X`, its constant quality scale (1 to 100, higher is better) inverted so that the search sees a QP-like scale (note: `-q:v`, not `-qp`), plus `-bsf:v dump_extra`: this encoder bakes the quality into the PPS and emits the parameter sets only in the container extradata, which the stream-copy concat keeps for the first segment only. Without the in-band copy before every keyframe, every segment after the first decodes against a foreign PPS: VideoToolbox decoding fails, software decoding outputs garbage. libx265 and NVENC do not need it, see the comment in `ffmpeg/hevc.go`.

These differences are subtle, encoder-specific, and break in different ways across ffmpeg versions. The `core.SegmentEncoder` interface enables testability, but the code behind it is intentionally explicit: the adapter about which function, preset and device each encoder gets, the encoder functions about every ffmpeg flag. Abstracting the adapter switch or the encoder functions behind a generic function would save a few dozen lines and cost hours of debugging when one encoder drifts. The codebase is **intentionally WET** (Write Explicit Twice) here, not DRY.

**Implication:** The ffmpeg invocation *is* the business logic here. Refactoring the adapter for DRYness would reduce auditability.

### The concat list states every segment's duration, and the concat snaps the timestamps

Segments are put back together by ffmpeg's concat demuxer, which starts each file where the previous one's duration ends. Left to the container, that duration is rounded to the millisecond, and at 23.976 fps the last frame always counts for 42 ms instead of 41.708: 0.3 ms too long per boundary on average (from 0.17 ms too short to 0.79 ms too long, depending on the segment's length), never compensated, so the video ended 46 ms behind the audio on a 163-segment episode and would end a quarter of a second behind on an 800-segment film. `ffmpeg.GenerateConcatList` therefore writes a `duration` line per file, computed in `core` from the segment's verified frame count and the frame rate (`QPSearchResults.SegmentsDurations`), and the `concat` command counts the packets of the files it is given for the same purpose. The frame counter's null muxer is what reveals a drift: a flood of `non monotonically increasing dts` while counting a merged file.

The duration lines alone still leave frames up to 1 ms off: the demuxer rounds each segment's exact start to the millisecond and adds it to timestamps the encoder already rounded from the segment's own start. At 23.976 fps some frames then last 43 ms (about 94% of simulated 163-segment episodes), which `IsConstantFrameRate` rightly calls variable: `sptenc vmaf` refused sptenc's own outputs. `ffmpeg.Concat` therefore snaps the video timestamps to the frame grid (`setts` bitstream filter) when given the frame rate, which every merge of the encode and batchsearch pipelines does (`QPSearchResults.FrameRate`). The `concat` command, given any files, does it only when they all declare the same frame rate and each starts with its video: otherwise the snap would corrupt the output (see `concatSnapFrameRate`).

**Implication:** neither the duration lines nor the snap are optional. Removing the lines, or feeding them container durations, reintroduces the drift, and the snap only corrects errors under half a frame. Removing the snap brings the 43 ms frames back. Any change to how segments are cut or merged must be checked by comparing the sorted video packet timestamps of the output with the master's, frame by frame: the expected difference is 0 ms everywhere. Check it with irregular segment lengths and real encodes: 144 frames last exactly 6006 ms at 23.976 fps and hide the rounding (the first check of the duration lines used such segments and missed it), and segments that are only cut, not encoded, keep the master's timestamps. Against the source itself, expect +1 ms on one frame in 24 if it was muxed by mkvmerge: it rounds half milliseconds down where ffmpeg rounds them up, the master's included. Matroska stores the frame duration in whole nanoseconds and ffmpeg reads it back as the closest fraction with terms up to 30000: 59.94 fps reads back as 19001/317 (119.88 as 29011/242, and so does any rate whose exact fraction has a larger term). From a source that is not Matroska, the master is numbered at the exact rate while its segments declare the approximation. Durations and snap built on the segments' rate put the output 1 ms off the master on one frame in 60 at 59.94 fps, more as the two rates drift apart (0.19 ms per hour), so the pipeline gives them the source's rate instead (`QPSearchConfig.SourceFrameRate`, compared with `SameFrameRate`). A Matroska source at those rates is read as the approximation from the start and its master is numbered at it: the output matches the master, and differs from the source's own timestamps the same way. The `concat` command only knows the source's rate when given the original file (`--original-file`, optional: without it the difference is about a millisecond on some frames): it snaps at the rate its files declare otherwise, and says so when that rate is a Matroska approximation of a usual one.

### VFR is rejected before encoding

Variable frame rate (VFR) content is rejected because the pipeline works on a constant frame grid: the encoders number the frames at the rate they read, and the concat rebuilds the timestamps from frame counts and the frame rate (duration lines and snap, see above). Measured on a Matroska file mixing 24 and 30 fps parts: the encode and its VMAF went fine (VMAF pairs frames by index, `-r` on its inputs, and the encoders run with `-fps_mode passthrough`, so no frame is dropped or duplicated), but the output had its 30 fps parts retimed to 24 fps, its video no longer lasting what its audio does (see `FFProbeBinaryStream.IsConstantFrameRate` in `ffmpeg/probe.go`).

The check runs twice: `checkSourceVideo` at startup compares the frame rates the file declares (a VFR Matroska file declares the same ones as a constant one), then `createMaster` compares the measured frame durations once every frame has been counted, before the master is written. A pre-split directory only gets the first check, on its first segment, plus the search refusing segments whose declared rates differ.

**Implication:** Adding VFR support means carrying every frame's own timestamp through the encodes and the concat, both of which rebuild a constant grid today. This is a design change, not a configuration flag.

### libaom-av1 is excluded

`libaom-av1` is too slow for iterative per-segment QP search, where each segment may be encoded multiple times. SVT-AV1 is the CPU AV1 encoder the CLI offers.

### VMAF v1 by default, on the CPU

Since v0.2.0 sptenc selects the VMAF v1 models (`vmaf_v1.0.16_3d0h` below 2160 lines, `vmaf_v1.0.16_1d5h_2160` from 2160). The v0 models are no longer selected, and `--vmaf-neg` and `--vmaf-cuda` were removed, on purpose, not left behind:

- v1 sees what v0 was blind to: posterized gradients (v0 scores them 100), chroma changes, banding (CAMBI is one of its features). Not everything: a grayscale copy still scores 100 (measurements in MANUAL.md, What VMAF sees). A quality floor tool has no reason to select a metric it knows is blind: the v0 models stay reachable with `--vmaf-model`, for comparisons (the published anchors were measured with them).
- NEG is built into v1 ("NEG is enabled by default for VMAF v1 without a need for a separate model", Netflix's announcement): the flag had nothing left to select.
- `libvmaf_cuda` only has the v0 features (ADM, VIF, motion: `libvmaf/src/feature/cuda`), and loading a v1 model in it fails on CAMBI. VMAF runs on the CPU; hardware decoding of its inputs stays. Measured on the episode above, `hevc_nvenc -C 6`: 11m13s of search with v0 on CUDA, 17m10s with v1 on the CPU. Accepted: the tool trades time for quality certainty, and CPU scores are reproducible where `libvmaf_cuda`'s were not.
- Scale: Netflix calibrated v1 to read like v0, but on both contents measured the same gate costs more. Harmonic mean 93, `hevc_nvenc`: video stream +62% on the anime episode, +101% on a 101 min live-action film; v0 scored the v1 encodes 95.0 and 95.6, v1 scored the v0 ones 91.0 and 90.1 (MANUAL.md, v1 against v0). The shift is broad (about 3 QP on average, 80% of the film's segments lost 2 to 5), and the gap in size is the content's price per QP: the segments that lost exactly 3 grew 1.44 times on the episode, 1.93 times on the film. The defaults still come from the v0 anchors, to be decided by the maintainer with these measurements.
- Selection by source height (`ffmpeg.SelectVMAFModel`), printed at the start, between the two models of `ffmpeg.VMAFModels`. The phone and 4K-at-3H models (lenient viewing conditions, the latter on a 0–110 scale) and the HFR variants are never selected, but `--vmaf-model` takes them, and any name libvmaf knows: a model released after sptenc is usable without an update. The name is only checked for what the filter graph would parse (`ffmpeg.VMAFModel.Valid`), whether libvmaf knows it is the preflight probe's job. The v0 models are named too (`ffmpeg.VMAFV0Models`: described, and warned about when forced) and go through the v1 code path: that is how the v0 anchors kept in MANUAL.md can still be reproduced. A report without CAMBI (a v0 model) prints no banding line, its zero would read as "no banding". A model made for another display than the source's is only a warning (contradicting the rule is the point of forcing it), a model sptenc does not know is said so. HDR models are not released yet.
- libvmaf 3.2.0 or newer is probed for real at preflight (`ffmpeg.VMAFProbe`: two 1080p `lavfi` frames scored against themselves). The proof is the report on disk, not ffmpeg's exit code: below its minimum size libvmaf can write no report while ffmpeg exits 0. `ffmpeg -version` does not carry the libvmaf version: a version gate on ffmpeg would be neither necessary nor sufficient.
- The picture size is probed for real too, once the source is known and before its master (`checkVMAFPictures`: two frames of the source's size, with the model of the run). libvmaf's minimum depends on the model and on the aspect ratio: measured, the selected models score 216×160 but not 1920×160 nor 3840×180, the phone ones need 480×270 and the 4K-at-3H ones 568×320 at 16:9. A fixed floor could not follow that, nor the next libvmaf. `master` and `split` do not score VMAF and have no size limit.
- The report parser picks the score (`vmaf`) and CAMBI (by prefix) out of the feature keys, which are templated from the model options and change with a retrained model.
- CAMBI is computed on the distorted picture alone (no reference): it rates the banding present in the encode, the source's included, not what the encoder added. It flags fine staircases in smooth areas (8-bit steps reach its cap of 17), not coarse posterization. It is printed as a diagnostic (pooled mean and max), never gated, and not part of the cache identity.
- A source that is banded already bends the QP curve. Measured on a 9 s dark scene of the film, from an 8-bit Blu-ray: 91.8 against itself (CAMBI 14.0), while its encodes are less banded by CAMBI's measure and score above it, peaking at 94.1 at QP 4 and back down to 93.7 at QP 0. The search passed hmean 93 at QP 9 (v0 was satisfied at QP 20, with a segment 15 times smaller). Replayed on that curve with the mock encoder of the core tests: a gate above the peak ends as best effort at qpMin, below the peak; a gate between the qpMin score and the peak ends as best effort from a cold start (encodes 26, 13, 0) although QPs 1 to 6 pass, and at QP 6 from a seed of 21 ± 3. Best effort keeping qpMin assumes the monotonic curve this breaks.

**Implication:** do not reintroduce a v0 selection, the NEG flag or a CUDA VMAF path: the v0 models are reachable through `--vmaf-model` only, like any other model. A model change is a cache identity change (the model name is in the cache file name and in the output tags), nothing else: v0.1.0 cache files are only read again by a run forcing the same v0 model.

### The master is FFV1, not a GPU-decodable lossless format

FFV1 is what keeps the decodes of every attempt (encode input, VMAF reference) on the CPU: no GPU decodes it. A lossless all-intra HEVC master (NVENC `-tune lossless -g 1`, or x265 `lossless=1:keyint=1`) keeps every property of the master (cut at any frame, bit-exact: checked frame by frame in software and on NVDEC) and NVDEC decodes it. Measured on the episode above (`hevc_nvenc -C 6`, VMAF v1, same segments cut from both masters): same QPs on all 163 segments and the same output, 65% less CPU time for the whole run, but 20m29s of search instead of 17m10s. NVDEC itself is the limit: 12 lossless segments decoded at once ran at 411 frames per second with NVDEC busy 87%, where the 32 threads of the CPU decode the same segments from FFV1 at 693. The HEVC master also takes 1.9 (x265 medium) to 2.6 times (NVENC) the space of FFV1, and the x265 flavor declares a range extensions profile (Main 10 Intra) that ffmpeg's VA-API and DXVA decoders refuse.

**Implication:** a second mastering path is not worth maintaining without a dramatic speed gain. Reopen it only with a new fact, such as a much faster hardware decoder.

### Cache isolation

Stats are stored per `(encoder, vmaf_model, vmaf_profile, optional_cache_profile)`. The filename is `qphistory_<encoder>.model~<model>_vmaf-<thresholds>[_<profile>].json`: the VMAF model is one of the two v1 names sptenc selects (`vmaf_v1.0.16_3d0h`, `vmaf_v1.0.16_1d5h_2160`), or the one forced with `--vmaf-model` (the file format did not change: a v0 name forced reads the cache sptenc v0.1.0 wrote under that name), `<thresholds>` is the URL-safe Base64 (no padding) of the 8 threshold values in the order `min|p1|p5|p10|p25|median|hmean|mean` (-1 when disabled), and the optional cache profile name is Base64 encoded the same way (`computeCacheStatsFileName` in `core/cache_persistent.go`). This is unambiguous and filesystem-safe across Windows/Linux/macOS.

Changing any VMAF threshold value by even 0.1 starts a fresh cache. This is correct — different thresholds require fundamentally different QP distributions.

### Cache layering: one persistent cache, ephemeral caches on top

Two cache types live in `core/`, and the ephemeral one is instantiated in two different places. Read this before touching any of them: the layering is not visible from any single file, and neither is the loop it forms with the search.

**The cache and the search are one feedback loop.** The search reads two numbers when it starts a segment: it encodes the mean first, then walks away from it in stddev-sized steps until it has bracketed the threshold (one QP passing, a higher one failing), then lets the predictor pick which QP inside the bracket to encode next. Every stddev of error in the mean is one more encode before the bracket closes. When the segment is done, its final QP goes back into the cache and moves both numbers for the next segment: the mean at once, the stddev from the second QP on (the base's spread and the spread of the run's own QPs, averaged with the same weights as the mean). So the search is only as good as the mean it starts from, and the mean is only right if it tracks this file rather than history. That is the whole reason for the seeding rule below.

A worked example, with the step logic of `searchSegmentQP` (run on the test mock with several curve shapes). A segment needs QP 22 (22 passes, 23 fails). Starting from history, mean 30 and stddev 4: 30 fails, 26 fails, 22 passes, the bracket [22, 26] is closed after three encodes. The forecast expects nothing above 22 to pass, so the search encodes the next QP up, 23, to confirm it fails. Four attempts. Starting from a run cache that already learned this file, mean 23 and stddev 3: 23 fails, 20 passes, the bracket [20, 23] is closed after two, one forecast-picked encode lands on 22 and 23 is already known to fail. Three attempts (four on the steepest curves, when the forecast lands on 21 first). The difference is paid on every segment for as long as the mean is wrong, which is why the mean must be the file's within a few segments.

**`core.StatsCacheHistory`** (`core/cache_persistent.go`) is the on-disk cache described above. It holds one entry per past encode job (mean QP, stddev, weight = number of segments), deduplicated, and aggregates them as runs weighted by their segment count. It is written once per job by `cmd/`: `encode` stores its results, `batchsearch` stores only the winning candidate (the other candidates are the same content encoded again and would over-represent that file), plus the final CPU encode on that encoder's own cache. The search itself never writes to it: `FindAllSegmentsQP` only reads.

**`core.EphemeralStatsCache`** (`core/cache_ephemeral.go`) is in-memory only and wraps any `StatsCache`, itself included. It appears at two levels:

1. **Per search.** `FindAllSegmentsQP` wraps whatever cache it is given for the duration of the search and adds each segment's final QP to it as soon as it is found (`addQP`). Every worker reads the mean and stddev from it when it starts a segment. This is where the learning happens: after a handful of segments the search starts from what this file needs, not from history.
2. **Per batchsearch run.** `batchsearch` wraps the persistent cache once for the whole run, gives that same wrapper to every candidate search (which wraps it again, per point 1), and feeds it each candidate's final QPs with `AddRun` when the candidate is done. So a candidate starts from what the previous candidates found on this very content, and only the first one pays the learning cost. During a candidate search the stack is: persistent → run ephemeral → search ephemeral.

**The seeding rule: a base weighs one ghost segment, whatever it holds.** When an ephemeral cache is created, its base's `Snapshot` (mean, stddev) counts as a single segment in the ephemeral statistics, be the base a persistent cache of thousands of segments or another ephemeral cache. The base is a starting point, not knowledge about this content: the persistent cache aggregates other files, whose mean is theirs. The run's own segments are the only measurement of this file, so they must take over as soon as they exist: after one segment the mean is halfway to it, after nine the base is noise.

This was measured (2026-09-22, real `FindAllSegmentsQP` over synthetic segments with curved responses, 5 seeds, concurrency 1 and 6) after a first design seeded the base with its real weight:
- Cache mean 6 QP away from the file's: seeding with the base's weight cost +0.7 attempt per segment (up to +2.1 at 12 QP away, +67% with a tight cache stddev). A weight of one cost +0.02 over a cold start.
- Cache mean matching the file: all weights within 0.03 attempt per segment; the base's full weight gained 0.07 at most.
- What the persistent cache buys over a cold start is a fixed cost on the first segments (and on the whole first wave with `-C`), when the content sits far from the QP range midpoint, and nothing when it sits near it: about 0.1 attempt per segment over a 160-segment episode (some 16 encodes, 2 to 3% of the attempts), up to 1.2 attempts per segment on a 6-segment clip (a third).

**Implications:**
- `StatsCache.Snapshot` returns no weight, on purpose: nothing may read one. Do not add it back to give a "solid" cache more say, that is the rejected design.
- The between-run weighting inside the persistent file (runs weighted by segment count) is a different thing and stays: it is how files are averaged with each other on disk, not how history is weighed against the current run.
- Do not invest in richer persistent statistics expecting large gains: the file overrides them within a handful of segments. The persistent cache is cheap insurance for the first segments, and its main value is being a better guess than the range midpoint.
- Segments searched concurrently can not learn from each other until they finish: the first `-C` segments all start from the seed. That costs attempts only while the seed is wrong. The first candidate of a `batchsearch` pays it on its first wave; the next candidates seed from the run cache, already on this content, so their first wave starts in the right place.

### Decoupling architecture

`core/` is fully decoupled from `ffmpeg/` behind the `core.SegmentEncoder` interface:

- `core.SegmentEncoder` abstracts `Name`, `QPRange`, `Encode`, `ComputeVMAF`, `ProbeStream` and `CountFrames`
- `pipeline.EncoderAdapter` implements this interface by delegating to concrete `ffmpeg` functions
- `core/` types (`VideoStream`, `VMAFStats`, `ProgressStats`, `Scene`) are owned by `core/` and have no `ffmpeg` imports

This means `core/` can be unit-tested with mocked encoders that return predetermined VMAF results — no real ffmpeg processes required.

**Guardrail:** Do not add new concrete `ffmpeg` imports into `core/`. That would re-tangle the packages and break the testing strategy.

### Testing strategy

`core/` is tested with unit tests (scenario tests, and table-driven ones where cases line up) that use a `mockEncoder` implementing `core.SegmentEncoder`. No real ffmpeg processes are invoked.

**Key test helpers (defined in `core/qpsearch_test.go`):**
- `mockEncoder` — returns pre-computed VMAF results from a `map[int]VMAFStats` (one for all segments, or one per segment with `vmafBySegment`). Creates dummy files on disk so `getFileSize` succeeds. Tracks all `Encode` and `ComputeVMAF` calls for call-count assertions. Safe for concurrent use, and tracks how many encodes run at the same time (`maxActive`) for the tests using several workers.
- `linearVMAF(qp)` — generates a monotonic VMAF curve (`mean = 100 - 1.5*qp`) for predictable convergence tests.
- `extractSegmentAndQP` — parses the segment index and the QP from encoded segment filenames (`seg_%06d_qp%03d.mkv`) so the mock can look up the right VMAF result.
- `mockStatsCache` — returns a fixed, configurable mean and stddev (its `Snapshot` always reports a history). `mockCallbacks` — no-op implementation of the progress callbacks. `recordingCallbacks` records worker IDs and reported totals, from any number of workers.

**Critical test files:**
- `core/qpsearch_test.go` — Convergence, best-effort fallback, cache guidance, ephemeral convergence, multi-segment runs, error propagation from `Encode`/`ComputeVMAF`/`ProbeStream`, `KeepInvalidQP` behavior.
- `core/predictor_test.go` — Interpolation accuracy, extrapolation clamping, VMAF 100 ceiling adaptation, monotonicity, insufficient-point errors.
- `core/cache_persistent_test.go` and `core/cache_ephemeral_test.go` — `AddRun` deduplication, concurrent access, save/load roundtrip, empty-cache heuristic, filename stability, the one-segment seed weight.
- `core/vmaf_test.go` — Checker construction errors, boundary values, active/inactive threshold combinations.
- `core/scenes_test.go` — Candidate generation, deduplication, `minDrop` spacing, `GetOptimalMinDrop` edge cases.
- `ffmpeg/vmaf_test.go` — Model names, selection and mismatch, filter string, v1 report parsing (templated keys, CAMBI), and the libvmaf probe against the real ffmpeg: every v1 model, unknown and invalid names, the minimum picture size of each kind of model (skipped when ffmpeg or the models are not there).

**Rule:** Any change to `core/` that lacks test coverage must be paired with a test. `core/` is where correctness guarantees live; the tests are the proof.

## Correct frame of reference

When evaluating changes, compare against:
- **Av1an** — targets parallelism and speed; sptenc targets quality certainty
- **Single-pass CRF + VMAF spot-checking** — sptenc guarantees per-segment floors; CRF guarantees averages
- **Commercial archival solutions** — compare feature sets and quality guarantees; sptenc targets the same niche with an open-source, locally-run model

## Final note

A change that looks like a small refactor (+10/-5 lines) can invalidate months of empirical tuning. If you are modifying the closed-loop workflow (QP search, threshold validation, scene splitting strategy, cache behavior, interpolation logic), read the critical files listed above first. Good changes start with understanding.
