# sptenc Context for Contributors and Coding Agents

**Read this before modifying any code.** It will save time and prevent broken assumptions.

## What this is

`sptenc` is a **perceptual-quality, VMAF-driven video encoder** written in Go. It targets a specific niche: **archival/preservation workflows where provable quality floors matter more than encoding speed**.

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
| `pipeline/` | `EncoderAdapter` — bridges `core.SegmentEncoder` to concrete `ffmpeg` encoder functions | The adapter *is* the ffmpeg invocation mapping; changes here must be audited against the exact encoder switch in `ffmpeg/`. |
| `metadata/` | `GenerateTags` — assembles ffmpeg metadata flags from `core` and `ffmpeg` results | Shared between CLI and any future front-ends (e.g. GUI). |
| `cli/` | urfave/cli v3 commands, orchestration, live progress UI | UX changes, new commands, workflow modifications. |
| `mkvtoolnix/` | `mkvpropedit` wrapper for metadata tagging | Metadata format changes. |

## Critical files to read before changing anything

- **`README.md`** — QP-vs-CRF rationale, cache system, GPU selection, all commands.
- **`cli/cmd_batchsearch.go`** — The **parameter discovery engine**, not a utility. GPU-accelerated sweeps to find the optimal scene detection threshold before slow CPU final encodes. Includes live progress UI and statistical decision logic.
- **`core/interfaces.go`** — `SegmentEncoder` interface contract; changes here affect both `core/` and `pipeline/`.
- **`core/qpsearch.go`** — Adaptive QP search algorithm. Statistical cache (mean/stddev) + Fritsch-Butland interpolation, converges in ~3–5 attempts per segment.
- **`core/predicator.go`** — Monotonic interpolation with empirical ceiling adaptation. Contains benchmark data in comments proving method selection.
- **`core/cache.go`** — Persistent QP history with profile isolation.
- **`pipeline/encoder.go`** — The **adapter** that maps `core.SegmentEncoder` to concrete ffmpeg encoder invocations. Contains the explicit per-encoder switch.
- **`ffmpeg/hevc.go` and `ffmpeg/av1.go`** — Encoder wrappers for libx265, NVENC, VAAPI, D3D12VA, VideoToolbox, SVT-AV1, libaom-av1.
- **`ffmpeg/vmaf.go`** — VMAF computation with CUDA-accelerated path (`libvmaf_cuda`) and NVDEC auto-detection.

## Key architectural constraints

### QP (Constant QP), not CRF

CQP applies the same base quantization to every frame, making QP→VMAF **monotonic and predictable**. This monotonicity enables interpolation search to converge in ~3–5 attempts per segment.

CRF varies QP frame-by-frame internally. The same CRF value produces different effective quantizations depending on scene complexity, making the search space noisy and unsuitable for interpolation.

**Implication:** Adding CRF modes, adaptive quantizers, or "hybrid CRF/QP" approaches would require redesigning the entire search algorithm, not just adding a flag.

### Segment encoding concurrency

Segments are encoded **sequentially by default**, but **optional concurrency** is supported via `QPSearchConfig.NbConcurrentSegments` (CLI: `--concurrent-segments` / `-C`). The CLI rejects concurrency greater than 1 for CPU encoders.

- **CPU encoders** (`libx265 slow`, `svtav1`) are limited to 1 concurrent segment. They already saturate physical cores on enthusiast hardware; concurrent instances thrash cache and memory bandwidth, reducing total throughput. The CLI enforces this limit with a hard error if a higher value is requested.
- **GPU encoders** benefit from concurrency because they often support multiple parallel sessions (typically 1–3 on consumer cards, SKU-dependent). The `batchsearch` command encourages raising this value for GPU-based threshold discovery, followed by an optional sequential CPU final encode (`--final-encode`) for maximum compression efficiency.

Concurrency is implemented as a worker pool (`golang.org/x/sync/errgroup`) in `core/qpsearch.go`, with worker-scoped callbacks so the UI can attribute progress to individual workers.

**Implication:** The default remains sequential for safety. Only raise concurrency when you know your hardware can sustain it. Naive goroutine-per-segment approaches would hurt performance or fail on hardware limits.

### Explicit per-encoder switches

`pipeline/encoder.go` contains a long, explicit `switch` that maps each encoder to its concrete ffmpeg invocation. This is intentional.

Each encoder uses **distinct ffmpeg semantics**:
- `libx265`: `-qp X -x265-params 'aq-mode=3'`
- NVENC: `-rc constqp -qp X -rc-lookahead 32`
- VAAPI: `-rc_mode CQP -qp X`
- VideoToolbox: `-q:v X` (note: `-q:v`, not `-qp`)

These differences are subtle, encoder-specific, and break in different ways across ffmpeg versions. The `core.SegmentEncoder` interface enables testability, but the adapter behind it is intentionally explicit about every ffmpeg flag. Abstracting the adapter switch behind a generic function would save ~30 lines and cost hours of debugging when one encoder drifts. The codebase is **intentionally WET** (Write Explicit Twice) here, not DRY.

**Implication:** The ffmpeg invocation *is* the business logic here. Refactoring the adapter for DRYness would reduce auditability.

### VFR is rejected at startup

Variable frame rate (VFR) content is rejected because VMAF requires frame-exact alignment between reference and distorted videos. VFR causes ffmpeg to duplicate or drop frames when forced to a constant rate, invalidating VMAF scores.

**Implication:** Adding VFR support requires solving the frame-exact alignment problem for VMAF computation. This is a hard problem, not a configuration flag.

### libaom-av1 is excluded

`libaom-av1` is too slow for iterative per-segment QP search, where each segment may be encoded multiple times. SVT-AV1 is the only viable CPU AV1 encoder for this workflow.

### Cache isolation

Stats are stored per `(encoder, vmaf_profile, optional_cache_profile)`. The filename is a Base64 hash of all threshold values + encoder + profile name. This is unambiguous and filesystem-safe across Windows/Linux/macOS.

Changing any VMAF threshold value by even 0.1 starts a fresh cache. This is correct — different thresholds require fundamentally different QP distributions.

### Decoupling architecture

`core/` is fully decoupled from `ffmpeg/` behind the `core.SegmentEncoder` interface:

- `core.SegmentEncoder` abstracts `Encode`, `ComputeVMAF`, `ProbeStream`, and `QPRange`
- `pipeline.EncoderAdapter` implements this interface by delegating to concrete `ffmpeg` functions
- `core/` types (`VideoStream`, `VMAFStats`, `ProgressStats`, `Scene`) are owned by `core/` and have no `ffmpeg` imports

This means `core/` can be unit-tested with mocked encoders that return predetermined VMAF results — no real ffmpeg processes required.

**Guardrail:** Do not add new concrete `ffmpeg` imports into `core/`. That would re-tangle the packages and break the testing strategy.

### Testing strategy

`core/` is tested with table-driven unit tests that use a `mockEncoder` implementing `core.SegmentEncoder`. No real ffmpeg processes are invoked.

**Key test helpers (defined in `core/qpsearch_test.go`):**
- `mockEncoder` — returns pre-computed VMAF results from a `map[int]VMAFStats`. Creates dummy files on disk so `getFileSize` succeeds. Tracks all `Encode` and `ComputeVMAF` calls for call-count assertions.
- `linearVMAF(qp)` — generates a monotonic VMAF curve (`mean = 100 - 1.5*qp`) for predictable convergence tests.
- `extractQPFromPath` — parses QP from segment filenames (`seg_%06d_qp%03d.mkv`) so the mock can look up the right VMAF result.
- `mockStatsCache` / `mockCallbacks` — no-op implementations for cache and progress injection.

**Critical test files:**
- `core/qpsearch_test.go` — Convergence, best-effort fallback, cache guidance, multi-segment runs, error propagation from `Encode`/`ComputeVMAF`/`ProbeStream`, `KeepInvalidQP` behavior.
- `core/predicator_test.go` — Interpolation accuracy, extrapolation clamping, VMAF 100 ceiling adaptation, monotonicity, insufficient-point errors.
- `core/cache_test.go` — `AddRun` deduplication, concurrent access, save/load roundtrip, empty-cache heuristic, filename stability.
- `core/vmaf_test.go` — Checker construction errors, boundary values, active/inactive threshold combinations.
- `core/scenes_test.go` — Candidate generation, deduplication, `minDrop` spacing, `GetOptimalMinDrop` edge cases.

**Rule:** Any change to `core/` that lacks test coverage must be paired with a test. `core/` is where correctness guarantees live; the tests are the proof.

## Correct frame of reference

When evaluating changes, compare against:
- **Av1an** — targets parallelism and speed; sptenc targets quality certainty
- **Single-pass CRF + VMAF spot-checking** — sptenc guarantees per-segment floors; CRF guarantees averages
- **Commercial archival solutions** — compare feature sets and quality guarantees; sptenc targets the same niche with an open-source, locally-run model

## About this codebase

- 2+ years of active development
- Deep empirical knowledge of ffmpeg, VMAF, encoder behavior, and hardware acceleration matrices
- Values correctness over speed; willing to accept compute cost for provable results
- The author has re-audited the encoder paths and search algorithm multiple times

## Final note

A change that looks like a small refactor (+10/-5 lines) can invalidate months of empirical tuning. If you are modifying the closed-loop workflow (QP search, threshold validation, scene splitting strategy, cache behavior, interpolation logic), read the critical files listed above first. Good changes start with understanding.
