package secretbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSealOpens(t *testing.T) {
	box := New([]byte("secret"), "tokens")
	sealed := box.Seal([]byte("refresh-token"))
	assert.NotContains(t, sealed, "refresh")

	plain, err := box.Open(sealed)
	require.NoError(t, err)
	assert.Equal(t, "refresh-token", string(plain))

	assert.NotEqual(t, sealed, box.Seal([]byte("refresh-token")), "a fresh nonce each time")
}

func TestOpenRejects(t *testing.T) {
	box := New([]byte("secret"), "tokens")
	sealed := box.Seal([]byte("refresh-token"))
	flipped := []byte(sealed)
	// Not the last character: its low bits are padding, so a flip there can decode to the same bytes.
	if flipped[20] == 'A' {
		flipped[20] = 'B'
	} else {
		flipped[20] = 'A'
	}

	for name, tc := range map[string]struct {
		box    *Box
		sealed string
	}{
		"another secret":  {New([]byte("other"), "tokens"), sealed},
		"another purpose": {New([]byte("secret"), "cookies"), sealed},
		"tampered":        {box, string(flipped)},
		"truncated":       {box, sealed[:8]},
		"not base64":      {box, "!!!"},
		"empty":           {box, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.box.Open(tc.sealed)
			assert.ErrorIs(t, err, ErrOpen)
		})
	}
}
