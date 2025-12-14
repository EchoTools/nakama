package evr

// CalculateSymbolValue calculates the hash value for a string with optional length limit.
// This is a direct translation of CSymbol64::CalculateSymbolValue from RAD engine.
// Parameters:
//
//	str: The string to hash
//	base: Initial hash value
//	precache: Lookup table for hash calculation
//	maxlen: Maximum number of characters to process (0 = no limit)
func CalculateSymbolValue(str string, base int64, precache [0x100]uint64, maxlen uint64) int64 {
	// If string is empty, return the base value unchanged
	if len(str) == 0 {
		return base
	}

	hash := base

	// Extract the high byte of the base hash for precache lookup
	highByte := uint8((hash >> 56) & 0xFF)

	remainingLength := maxlen

	// Process each character in the string
	for i := 0; i < len(str); i++ {
		// Check if we've reached the maximum length
		if maxlen > 0 && remainingLength == 0 {
			break
		}
		if maxlen > 0 {
			remainingLength--
		}

		ch := str[i]

		// XOR the shifted hash with the precache value indexed by high byte
		shifted := (uint64(hash) << 8) ^ precache[highByte]

		// Convert uppercase letters (A-Z, ASCII 65-90) to lowercase
		// Check if character is in range [65, 90] (A-Z)
		if uint8(ch-65) <= 25 {
			ch += 32 // Convert to lowercase
		}

		// XOR the character with the shifted/precached value
		hash = int64(ch) ^ int64(shifted)

		// Update high byte for next iteration
		highByte = uint8((hash >> 56) & 0xFF)
	}

	return hash
}
