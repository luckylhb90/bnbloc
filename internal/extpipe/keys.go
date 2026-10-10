package extpipe

import "github.com/luckylhb90/bnbloc/internal/extreg"

const (
	recordKeyPrefix = "ext."
	recordKeySuffix = ".installed"
	trustedKeysKey  = "ext.trustedKeys"
)

func recordKey(kind extreg.Kind, name string) string {
	return recordKeyPrefix + string(kind) + "." + name + recordKeySuffix
}
