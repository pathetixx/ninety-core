//go:build with_utls

package tls

import (
	"crypto/rand"
	"math/big"
	"strings"

	utls "github.com/metacubex/utls"
)

// mixedCaseServerName randomises the letter case of a hostname. DNS names are
// case-insensitive, so this changes neither the server the handshake reaches nor
// the certificate check, but a filter comparing the SNI byte-for-byte no longer
// matches.
func mixedCaseServerName(serverName string) string {
	if serverName == "" {
		return serverName
	}
	var builder strings.Builder
	builder.Grow(len(serverName))
	for _, char := range serverName {
		switch {
		case char >= 'a' && char <= 'z':
			if randBool() {
				char -= 'a' - 'A'
			}
		case char >= 'A' && char <= 'Z':
			if randBool() {
				char += 'a' - 'A'
			}
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

func randBool() bool {
	value, err := rand.Int(rand.Reader, big.NewInt(2))
	if err != nil {
		return false
	}
	return value.Int64() == 1
}

// randPaddingLen draws a padding length in [from, to] for every handshake, so
// the ClientHello does not keep landing on the one length its fingerprint would
// otherwise always produce.
func randPaddingLen(from, to int) int {
	if to <= from {
		return from
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(to-from+1)))
	if err != nil {
		return from
	}
	return from + int(value.Int64())
}

// paddedSpec returns the fingerprint's ClientHello with its padding extension
// replaced by one of our own size. uTLS applies a preset spec only through
// HelloCustom, which is why the caller has to hand the connection this spec
// instead of the fingerprint id.
func paddedSpec(id utls.ClientHelloID, from, to int) (*utls.ClientHelloSpec, error) {
	spec, err := utls.UTLSIdToSpec(id)
	if err != nil {
		return nil, err
	}
	padding := &utls.UtlsPaddingExtension{
		GetPaddingLen: func(clientHelloUnpaddedLen int) (paddingLen int, willPad bool) {
			return randPaddingLen(from, to), true
		},
	}
	for i, extension := range spec.Extensions {
		if _, isPadding := extension.(*utls.UtlsPaddingExtension); isPadding {
			spec.Extensions[i] = padding
			return &spec, nil
		}
	}
	spec.Extensions = append(spec.Extensions, padding)
	return &spec, nil
}
