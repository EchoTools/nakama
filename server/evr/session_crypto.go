package evr

/*
type PacketEncoderConfig struct {
	HashSize           int
	HashIterationCount int
	HashSecret         []byte
	CryptSecret        []byte
	StreamSecret       []byte
	HMACAlgorithm      int
	HMACKeySize        int
	StreamSeedSize     int
	EncryptionKeySize  int
}

func (p PacketEncoderConfig) EncKeySize() int {
	return p.EncryptionKeySize * 8
}

type PacketEncoderOptions struct {
	StreamSeedSize     int
	HMACAlgorithm      int
	HMACKeySize        int
	HashIterationCount int
	EncryptionKeySize  int
}

func (p *PacketEncoderConfig) ToFlags() uint64 {
	var flags uint64

	// Set bit 0 if encryption enabled
	if p.StreamSeedSize != 0 {
		flags |= 1
	}
	// Set bit 1 if MAC enabled
	if p.HMACAlgorithm != 0 {
		flags |= 2
	}
	// Bits 2-13: MACDigestSize
	flags |= (uint64(p.HMACKeySize) & 0xFFF) << 2
	// Bits 14-25: MACPBKDF2IterationCount
	flags |= (uint64(p.HashIterationCount) & 0xFFF) << 14
	// Bits 26-37: MACKeySize
	flags |= (uint64(p.HMACKeySize) & 0xFFF) << 26
	// Bits 38-49: EncryptionKeySize
	flags |= (uint64(p.EncryptionKeySize) & 0xFFF) << 38
	// Bits 50-61: RandomKeySize
	flags |= (uint64(len(p.CryptSecret)) & 0xFFF) << 50

	return flags
}

// CreateCryptoConfig derives keys and populates the CryptoConfig struct.
func CreateCryptoConfig(sessionKey, salt []byte, iterations int, encryptionKeySize, macKeySize, randomKeySize, macDigestSize int, initialSequenceId uint64) CryptoConfig {
	// 1. Derive enough key material for all three keys.
	derivedMaterial := pbkdf2.Key(sessionKey, salt, iterations, encryptionKeySize+macKeySize, sha512.New)

	// 3. Populate the final config struct.
	return CryptoConfig{
		SequenceID: initialSequenceId,
		EncKey:     derivedMaterial[0:encryptionKeySize],
		MacKey:     derivedMaterial[encryptionKeySize : encryptionKeySize+macKeySize],
		RandomKey:  derivedMaterial[encryptionKeySize+macKeySize:],
	}
}

*/
