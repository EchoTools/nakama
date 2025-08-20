# Float64 Leaderboard Score Encoding

## Overview

This document describes the float64 encoding system for Nakama leaderboard scores that supports negative values while maintaining proper sort order and high precision.

## Problem Statement

Traditional leaderboard systems only support non-negative integer scores. This limitation prevents storing player statistics that can be negative, such as:
- Performance deltas (improvement/decline metrics)
- K/D ratios below 1.0
- Penalty scores
- Rating changes
- Temperature or other measurements that can go below zero

## Solution

The float64 encoding system uses both the `score` and `subscore` fields of Nakama leaderboard records to encode a complete float64 value, including negative numbers, while ensuring:

1. **Non-negative constraint compliance**: All encoded values are non-negative integers
2. **Sort order preservation**: Encoded values sort identically to original float64 values
3. **High precision**: ~1e-9 fractional accuracy using 1e9 scaling factor
4. **Wide range support**: Values from -999999999999999 to +1e15

## Algorithm

The encoding uses an offset-based approach with two ranges:

- **Negative values**: Mapped to score range [0, 1e15)
- **Zero and positive values**: Mapped to score range [1e15, ∞)

### Encoding Process

```
For negative values (f < 0):
  absF = -f
  intPart = floor(absF)
  fracPart = absF - intPart
  score = 1e15 - 1 - intPart
  subscore = (1.0 - fracPart) * (1e9 - 1)

For zero and positive values (f >= 0):  
  intPart = floor(f)
  fracPart = f - intPart
  score = 1e15 + intPart
  subscore = fracPart * 1e9
```

### Examples

| Float64 Value | Score | Subscore | Explanation |
|---------------|-------|----------|-------------|
| -2.5 | 999999999999997 | 499999999 | Negative: more negative = smaller score |
| -1.0 | 999999999999999 | 0 | Negative integer |
| -0.1 | 999999999999999 | 899999999 | Small negative |
| 0.0 | 1000000000000000 | 0 | Zero baseline |
| 0.1 | 1000000000000000 | 100000000 | Small positive |
| 1.0 | 1000000000000001 | 0 | Positive integer |
| 2.5 | 1000000000000002 | 500000000 | Positive: larger = larger score |

## Implementation Examples

### Go Implementation

```go
package main

import (
    "fmt"
    "math"
)

const (
    LeaderboardScoreScalingFactor = float64(1000000000) // 1e9
    ScoreOffset = int64(1e15)
)

// Float64ToScore converts a float64 to (score, subscore) pair
func Float64ToScore(f float64) (int64, int64, error) {
    // Check for invalid values
    if math.IsNaN(f) || math.IsInf(f, 0) {
        return 0, 0, fmt.Errorf("invalid value: %f", f)
    }
    
    // Limit to reasonable range
    if f > 1e15 || f < -1e15 {
        return 0, 0, fmt.Errorf("value out of range: %f", f)
    }

    const fracScale = LeaderboardScoreScalingFactor
    const scoreOffset = ScoreOffset
    
    if f < 0 {
        // Negative numbers: use lower range [0, scoreOffset)
        absF := -f
        intPart := int64(absF)
        fracPart := absF - float64(intPart)
        
        score := scoreOffset - 1 - intPart
        subscore := int64((1.0 - fracPart) * float64(fracScale - 1))
        
        return score, subscore, nil
    } else {
        // Zero and positive numbers: use upper range [scoreOffset, ∞)
        intPart := int64(f)
        fracPart := f - float64(intPart)
        
        score := scoreOffset + intPart
        subscore := int64(fracPart * fracScale)
        
        return score, subscore, nil
    }
}

// ScoreToFloat64 converts (score, subscore) pair back to float64
func ScoreToFloat64(score int64, subscore int64) (float64, error) {
    // Validate inputs
    if score < 0 {
        return 0, fmt.Errorf("invalid score: %d (must be non-negative)", score)
    }
    if subscore < 0 || subscore >= int64(LeaderboardScoreScalingFactor) {
        return 0, fmt.Errorf("invalid subscore: %d", subscore)
    }

    const fracScale = LeaderboardScoreScalingFactor
    const scoreOffset = ScoreOffset
    
    if score < scoreOffset {
        // Negative number: score in range [0, scoreOffset)
        intPart := scoreOffset - 1 - score
        fracPart := 1.0 - (float64(subscore) / float64(fracScale - 1))
        return -(float64(intPart) + fracPart), nil
    } else {
        // Zero or positive number: score in range [scoreOffset, ∞)
        intPart := score - scoreOffset
        fracPart := float64(subscore) / fracScale
        return float64(intPart) + fracPart, nil
    }
}

func main() {
    // Example usage
    value := -2.5
    score, subscore, err := Float64ToScore(value)
    if err != nil {
        panic(err)
    }
    
    decoded, err := ScoreToFloat64(score, subscore)
    if err != nil {
        panic(err)
    }
    
    fmt.Printf("Original: %.9f\n", value)
    fmt.Printf("Encoded: score=%d, subscore=%d\n", score, subscore)
    fmt.Printf("Decoded: %.9f\n", decoded)
    fmt.Printf("Round-trip error: %.2e\n", math.Abs(decoded - value))
}
```

### Python Implementation

```python
import math

LEADERBOARD_SCORE_SCALING_FACTOR = 1000000000  # 1e9
SCORE_OFFSET = 1000000000000000  # 1e15

def float64_to_score(f):
    """Convert a float64 to (score, subscore) pair"""
    # Check for invalid values
    if math.isnan(f) or math.isinf(f):
        raise ValueError(f"Invalid value: {f}")
    
    # Limit to reasonable range
    if f > 1e15 or f < -1e15:
        raise ValueError(f"Value out of range: {f}")

    frac_scale = LEADERBOARD_SCORE_SCALING_FACTOR
    score_offset = SCORE_OFFSET
    
    if f < 0:
        # Negative numbers: use lower range [0, score_offset)
        abs_f = -f
        int_part = int(abs_f)
        frac_part = abs_f - int_part
        
        score = score_offset - 1 - int_part
        subscore = int((1.0 - frac_part) * (frac_scale - 1))
        
        return score, subscore
    else:
        # Zero and positive numbers: use upper range [score_offset, ∞)
        int_part = int(f)
        frac_part = f - int_part
        
        score = score_offset + int_part
        subscore = int(frac_part * frac_scale)
        
        return score, subscore

def score_to_float64(score, subscore):
    """Convert (score, subscore) pair back to float64"""
    # Validate inputs
    if score < 0:
        raise ValueError(f"Invalid score: {score} (must be non-negative)")
    if subscore < 0 or subscore >= LEADERBOARD_SCORE_SCALING_FACTOR:
        raise ValueError(f"Invalid subscore: {subscore}")

    frac_scale = LEADERBOARD_SCORE_SCALING_FACTOR
    score_offset = SCORE_OFFSET
    
    if score < score_offset:
        # Negative number: score in range [0, score_offset)
        int_part = score_offset - 1 - score
        frac_part = 1.0 - (subscore / (frac_scale - 1))
        return -(int_part + frac_part)
    else:
        # Zero or positive number: score in range [score_offset, ∞)
        int_part = score - score_offset
        frac_part = subscore / frac_scale
        return int_part + frac_part

# Example usage
if __name__ == "__main__":
    value = -2.5
    score, subscore = float64_to_score(value)
    decoded = score_to_float64(score, subscore)
    
    print(f"Original: {value:.9f}")
    print(f"Encoded: score={score}, subscore={subscore}")
    print(f"Decoded: {decoded:.9f}")
    print(f"Round-trip error: {abs(decoded - value):.2e}")
```

### C# Implementation

```csharp
using System;

public static class Float64ScoreEncoder
{
    private const double LEADERBOARD_SCORE_SCALING_FACTOR = 1000000000; // 1e9
    private const long SCORE_OFFSET = 1000000000000000; // 1e15

    /// <summary>
    /// Convert a float64 to (score, subscore) pair
    /// </summary>
    public static (long score, long subscore) Float64ToScore(double f)
    {
        // Check for invalid values
        if (double.IsNaN(f) || double.IsInfinity(f))
        {
            throw new ArgumentException($"Invalid value: {f}");
        }
        
        // Limit to reasonable range
        if (f > 1e15 || f < -1e15)
        {
            throw new ArgumentException($"Value out of range: {f}");
        }

        const double fracScale = LEADERBOARD_SCORE_SCALING_FACTOR;
        const long scoreOffset = SCORE_OFFSET;
        
        if (f < 0)
        {
            // Negative numbers: use lower range [0, scoreOffset)
            double absF = -f;
            long intPart = (long)absF;
            double fracPart = absF - intPart;
            
            long score = scoreOffset - 1 - intPart;
            long subscore = (long)((1.0 - fracPart) * (fracScale - 1));
            
            return (score, subscore);
        }
        else
        {
            // Zero and positive numbers: use upper range [scoreOffset, ∞)
            long intPart = (long)f;
            double fracPart = f - intPart;
            
            long score = scoreOffset + intPart;
            long subscore = (long)(fracPart * fracScale);
            
            return (score, subscore);
        }
    }

    /// <summary>
    /// Convert (score, subscore) pair back to float64
    /// </summary>
    public static double ScoreToFloat64(long score, long subscore)
    {
        // Validate inputs
        if (score < 0)
        {
            throw new ArgumentException($"Invalid score: {score} (must be non-negative)");
        }
        if (subscore < 0 || subscore >= (long)LEADERBOARD_SCORE_SCALING_FACTOR)
        {
            throw new ArgumentException($"Invalid subscore: {subscore}");
        }

        const double fracScale = LEADERBOARD_SCORE_SCALING_FACTOR;
        const long scoreOffset = SCORE_OFFSET;
        
        if (score < scoreOffset)
        {
            // Negative number: score in range [0, scoreOffset)
            long intPart = scoreOffset - 1 - score;
            double fracPart = 1.0 - (subscore / (fracScale - 1));
            return -(intPart + fracPart);
        }
        else
        {
            // Zero or positive number: score in range [scoreOffset, ∞)
            long intPart = score - scoreOffset;
            double fracPart = subscore / fracScale;
            return intPart + fracPart;
        }
    }
}

// Example usage
class Program
{
    static void Main()
    {
        double value = -2.5;
        var (score, subscore) = Float64ScoreEncoder.Float64ToScore(value);
        double decoded = Float64ScoreEncoder.ScoreToFloat64(score, subscore);
        
        Console.WriteLine($"Original: {value:F9}");
        Console.WriteLine($"Encoded: score={score}, subscore={subscore}");
        Console.WriteLine($"Decoded: {decoded:F9}");
        Console.WriteLine($"Round-trip error: {Math.Abs(decoded - value):E2}");
    }
}
```

## Usage Guidelines

### Leaderboard Creation

When creating leaderboards that will use float64 encoding:

```go
// Create leaderboard with proper configuration
err := nk.LeaderboardCreate(ctx, leaderboardID, true, "desc", "set", "", nil, true)
```

### Writing Records

```go
// Convert your float64 statistic to score/subscore
value := -15.7  // Player's K/D ratio
score, subscore, err := Float64ToScore(value)
if err != nil {
    return err
}

// Write to leaderboard
_, err = nk.LeaderboardRecordWrite(ctx, leaderboardID, userID, username, score, subscore, nil, nil)
```

### Reading Records

```go
// Read leaderboard records
_, records, _, _, err := nk.LeaderboardRecordsList(ctx, leaderboardID, []string{userID}, 1, "", 0)
if err != nil {
    return err
}

// Convert back to float64
if len(records) > 0 {
    record := records[0]
    originalValue, err := ScoreToFloat64(record.Score, record.Subscore)
    if err != nil {
        return err
    }
    
    fmt.Printf("Player's statistic: %.6f\n", originalValue)
}
```

## Limitations and Considerations

1. **Range**: Values must be between -999999999999999 and +1e15
2. **Precision**: Fractional precision is ~1e-9 (9 decimal places)
3. **Leaderboard Configuration**: Both ascending and descending sort work correctly
4. **Migration**: Existing leaderboards using single-value encoding need migration
5. **Client Libraries**: Client applications need to implement the same encoding/decoding logic

## Testing Sort Order

To verify correct sort behavior, test with mixed positive/negative values:

```
Test Values: [-10.5, -1.2, -0.1, 0.0, 0.1, 1.2, 10.5]

Ascending Sort Result:
  Rank 1: -10.5 → (999999999999989, 499999999)
  Rank 2: -1.2  → (999999999999998, 799999999) 
  Rank 3: -0.1  → (999999999999999, 899999999)
  Rank 4: 0.0   → (1000000000000000, 0)
  Rank 5: 0.1   → (1000000000000000, 100000000)
  Rank 6: 1.2   → (1000000000000001, 200000000)
  Rank 7: 10.5  → (1000000000000010, 500000000)
```

The encoded scores sort in the exact same order as the original float64 values.