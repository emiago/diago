// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"bytes"
	"io"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBridgeProxy(t *testing.T) {
	b := NewBridge()
	b.waitDialogsNum = 99 // Do not start proxy

	incoming := &DialogServerSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{
				Codecs: []media.Codec{media.CodecAudioAlaw},
			},
			audioReader:     bytes.NewBuffer(make([]byte, 9999)),
			audioWriter:     bytes.NewBuffer(make([]byte, 0)),
			RTPPacketReader: media.NewRTPPacketReader(nil, media.CodecAudioAlaw),
			RTPPacketWriter: media.NewRTPPacketWriter(nil, media.CodecAudioAlaw),
		},
	}
	outgoing := &DialogClientSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{
				Codecs: []media.Codec{media.CodecAudioAlaw},
			},
			audioReader:     bytes.NewBuffer(make([]byte, 9999)),
			audioWriter:     bytes.NewBuffer(make([]byte, 0)),
			RTPPacketReader: media.NewRTPPacketReader(nil, media.CodecAudioAlaw),
			RTPPacketWriter: media.NewRTPPacketWriter(nil, media.CodecAudioAlaw),
		},
	}

	err := b.AddDialogSession(incoming)
	require.NoError(t, err)
	err = b.AddDialogSession(outgoing)
	require.NoError(t, err)

	err = b.proxyMedia()
	require.ErrorIs(t, err, io.EOF)

	// Confirm all data is proxied
	assert.Equal(t, 9999, incoming.audioWriter.(*bytes.Buffer).Len())
	assert.Equal(t, 9999, outgoing.audioWriter.(*bytes.Buffer).Len())
}

func TestBridgeNoTranscodingAllowed(t *testing.T) {
	b := NewBridge()
	// b.waitDialogsNum = 99 // Do not start proxy

	incoming := &DialogServerSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{
				Codecs: []media.Codec{media.CodecAudioAlaw},
			},
			// RTPPacketReader: media.NewRTPPacketReader(nil, media.CodecAudioAlaw),
			// RTPPacketWriter: media.NewRTPPacketWriter(nil, media.CodecAudioAlaw),
		},
	}
	outgoing := &DialogClientSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{
				Codecs: []media.Codec{media.CodecAudioUlaw},
			},
			// RTPPacketReader: media.NewRTPPacketReader(nil, media.CodecAudioUlaw),
			// RTPPacketWriter: media.NewRTPPacketWriter(nil, media.CodecAudioUlaw),
		},
	}

	err := b.AddDialogSession(incoming)
	require.NoError(t, err)
	err = b.AddDialogSession(outgoing)
	require.Error(t, err)
}
