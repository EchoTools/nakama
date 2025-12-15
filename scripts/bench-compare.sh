#!/bin/bash
# Script to compare benchmark results against baseline and detect regressions
# Usage: ./scripts/bench-compare.sh [threshold_percent]

set -e

THRESHOLD=${1:-10}  # Default to 10% regression threshold
BASELINE_FILE="_benchmarks/predict_outcomes_baseline.txt"
NEW_BENCH_FILE="/tmp/bench_new_$$.txt"
BENCHSTAT=$(go env GOPATH)/bin/benchstat

# Check if benchstat is installed
if [ ! -f "$BENCHSTAT" ]; then
    echo "Installing benchstat..."
    go install golang.org/x/perf/cmd/benchstat@latest
fi

# Check if baseline exists
if [ ! -f "$BASELINE_FILE" ]; then
    echo "ERROR: Baseline file not found: $BASELINE_FILE"
    echo "Run 'make bench-baseline' to create one"
    exit 1
fi

echo "Running benchmarks (this may take a while)..."
go test -run='^$' -bench='BenchmarkPredictOutcomes$' -benchmem -count=6 ./server/ 2>&1 | \
  grep -E '^(goos|goarch|pkg|cpu|Benchmark|PASS|ok)' > "$NEW_BENCH_FILE"

echo ""
echo "Comparing against baseline..."
echo "Regression threshold: ${THRESHOLD}%"
echo "================================================"

# Run benchstat and capture output
COMPARISON=$("$BENCHSTAT" "$BASELINE_FILE" "$NEW_BENCH_FILE")
echo "$COMPARISON"

# Parse for regressions in time (sec/op)
REGRESSION=$(echo "$COMPARISON" | grep -A1 "sec/op" | grep "PredictOutcomes" | awk '{print $4}')

if [ -n "$REGRESSION" ]; then
    # Extract percentage if present (e.g., "+12.5%")
    if [[ "$REGRESSION" =~ \+([0-9.]+)% ]]; then
        PERCENT="${BASH_REMATCH[1]}"
        # Use bc for floating point comparison
        if (( $(echo "$PERCENT > $THRESHOLD" | bc -l) )); then
            echo ""
            echo "❌ PERFORMANCE REGRESSION DETECTED: +${PERCENT}%"
            echo "   This exceeds the threshold of ${THRESHOLD}%"
            rm -f "$NEW_BENCH_FILE"
            exit 1
        else
            echo ""
            echo "✅ Performance change within acceptable threshold: +${PERCENT}% (limit: ${THRESHOLD}%)"
        fi
    elif [[ "$REGRESSION" =~ -([0-9.]+)% ]]; then
        echo ""
        echo "✅ Performance IMPROVED: ${REGRESSION}"
    else
        echo ""
        echo "✅ No significant performance change detected"
    fi
else
    echo ""
    echo "✅ No significant performance change detected"
fi

rm -f "$NEW_BENCH_FILE"
exit 0
