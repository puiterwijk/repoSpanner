package protobuf

// FeatureBit is the type for feature bits
type FeatureBit uint64

const (
	// FeatureBitPackSync indicates support for "packsync", which syncs pushes as a single pack file
	FeatureBitPackSync FeatureBit = 0
)

func featureBitValue(bit FeatureBit) uint64 {
	return uint64(1 << bit)
}

// HasFeature returns whether a particular bit is set in the ping message
func (p *PingMessage) HasFeature(feature FeatureBit) bool {
	return p.GetFeatureBits()&featureBitValue(feature) != 0
}

// SupportedFeatures returns the set of features supported by this build
func SupportedFeatures() uint64 {
	return (featureBitValue(FeatureBitPackSync) |
		0)
}
