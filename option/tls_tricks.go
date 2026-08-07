package option

import (
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

// TLSTricksOptions are per-outbound tweaks to the TLS ClientHello aimed at DPI
// that matches on it. They change what the handshake looks like on the wire, not
// what it negotiates.
type TLSTricksOptions struct {
	// MixedCaseSNI randomises the letter case of the server name sent in SNI.
	// Hostnames are case-insensitive, so the handshake and the certificate check
	// are unaffected, but a filter comparing the SNI byte-for-byte is not.
	MixedCaseSNI bool `json:"mixedcase_sni,omitempty"`
	// PaddingSize is a "from-to" byte range for the ClientHello padding
	// extension, drawn again for every handshake. It moves the message off the
	// length its fingerprint would otherwise always produce.
	PaddingSize string `json:"padding_size,omitempty"`
}

// ParsePaddingSize reads the "from-to" form. An empty value means no padding.
func (o *TLSTricksOptions) ParsePaddingSize() (from int, to int, err error) {
	if o == nil || o.PaddingSize == "" {
		return 0, 0, nil
	}
	parts := strings.Split(o.PaddingSize, "-")
	if len(parts) > 2 {
		return 0, 0, E.New("invalid padding_size: ", o.PaddingSize)
	}
	from, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, E.Cause(err, "invalid padding_size: ", o.PaddingSize)
	}
	to = from
	if len(parts) == 2 {
		to, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, E.Cause(err, "invalid padding_size: ", o.PaddingSize)
		}
	}
	if from < 0 || to < from {
		return 0, 0, E.New("invalid padding_size: ", o.PaddingSize)
	}
	return from, to, nil
}
