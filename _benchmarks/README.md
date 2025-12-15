# Performance Benchmarks

This directory contains baseline benchmark results for performance regression testing.

## Quick Start

### Check for Performance Regressions

```bash
make bench-check
```

This will:
1. Run the benchmarks (takes ~30-40 seconds)
2. Compare against the baseline
3. Fail if performance regresses by more than 10%

### Update Baseline

After making intentional performance improvements:

```bash
make bench-baseline
```

### Custom Threshold

To use a different regression threshold (e.g., 5%):

```bash
./scripts/bench-compare.sh 5
```

## Current Baseline

**File:** `predict_outcomes_baseline.txt`

**Benchmark:** `BenchmarkPredictOutcomes`

| Metric | Baseline Value |
|--------|----------------|
| **Time** | ~6.45 sec/op |
| **Candidates** | 735,471 |
| **Predictions** | 1,470,942 |
| **Memory** | ~2.0 GiB/op |
| **Allocations** | ~28.7M allocs/op |

## Understanding Results

When you run `make bench-check`, `benchstat` will show:

- `~` = No significant change
- `+X%` = Performance regression (slower)
- `-X%` = Performance improvement (faster)
- `(p=0.xxx n=N)` = Statistical confidence

## CI Integration

Add to your CI pipeline:

```yaml
- name: Performance Regression Check
  run: make bench-check
```

This will fail the build if performance regresses beyond the threshold.

## Files

- `predict_outcomes_baseline.txt` - Baseline for matchmaker prediction benchmarks
- `README.md` - This file

## Notes

- Benchmarks use `-count=6` for statistical reliability
- Default regression threshold is 10%
- Results may vary slightly between runs due to system load
- Compare benchmarks on the same machine for accurate results
