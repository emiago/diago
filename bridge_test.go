package diago

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBridgeProxy(t *testing.T) {
	b := NewBridge()
	b.minDialogsNumber = 99 // Do not start proxy

	incoming := &DialogServerSession{
		DialogMedia: DialogMedia{
			audioReader: bytes.NewBuffer(make([]byte, 100)),
			audioWriter: bytes.NewBuffer(make([]byte, 0)),
		},
	}
	outgoing := &DialogClientSession{
		DialogMedia: DialogMedia{
			audioReader: bytes.NewBuffer(make([]byte, 100)),
			audioWriter: bytes.NewBuffer(make([]byte, 0)),
		},
	}

	err := b.AddDialogSession(incoming)
	require.NoError(t, err)
	err = b.AddDialogSession(outgoing)
	require.NoError(t, err)

	err = b.proxyMedia()
	require.ErrorIs(t, err, io.EOF)

	// Confirm all data is proxied
	assert.Equal(t, 100, incoming.audioWriter.(*bytes.Buffer).Len())
	assert.Equal(t, 100, outgoing.audioWriter.(*bytes.Buffer).Len())
}
