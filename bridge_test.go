// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ulawDecode(b []byte) []byte {
	dec := make([]byte, len(b)*2)
	n, _ := audio.DecodeUlawTo(dec, b)
	return dec[:n]
}

func ulawEncode(b []byte) []byte {
	dec := make([]byte, len(b)/2)
	n, _ := audio.EncodeUlawTo(dec, b)
	return dec[:n]
}
func TestBridgeProxy(t *testing.T) {
	b := NewBridge()
	b.WaitDialogsNum = 99 // Do not start proxy

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

func TestIntegrationBridging(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Create transaction users, as many as needed.
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("inbound"),
	)
	defer ua.Close()
	tu := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  5090,
		},
	))

	err := tu.ServeBackground(ctx, func(in *DialogServerSession) {
		in.Trying()
		in.Ringing()
		in.Answer()

		inCtx := in.Context()
		ctx, cancel := context.WithTimeout(inCtx, 15*time.Second)
		defer cancel()

		// Wa want to bridge this call with originator
		bridge := NewBridge()
		// Add us in bridge
		if err := bridge.AddDialogSession(in); err != nil {
			t.Log("Adding dialog in bridge failed", err)
			return
		}

		out, err := tu.InviteBridge(ctx, sip.Uri{User: "test", Host: "127.0.0.200", Port: 5090}, &bridge, InviteOptions{})
		if err != nil {
			t.Log("Dialing failed", err)
			return
		}

		outCtx := out.Context()
		defer func() {
			hctx, hcancel := context.WithTimeout(outCtx, 5*time.Second)
			out.Hangup(hctx)
			hcancel()
		}()

		// This is beauty, as you can even easily detect who hangups
		select {
		case <-inCtx.Done():
		case <-outCtx.Done():
		}

		// How to now do bridging
	})
	assert.NoError(t, err)

	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.200",
				BindPort:  5090,
			},
		))

		err := dg.ServeBackground(context.Background(), func(d *DialogServerSession) {
			ctx := d.Context()
			err = d.Answer()
			require.NoError(t, err)

			// ms := d.mediaSession
			buf := make([]byte, media.RTPBufSize)
			r, _ := d.AudioReader()
			n, err := r.Read(buf)
			require.NoError(t, err)

			w, _ := d.AudioWriter()
			w.Write(buf[:n])
			require.NoError(t, err)

			<-ctx.Done()
		})
		require.NoError(t, err)
	}

	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := newDialer(ua)
		dialog, err := dg.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, InviteOptions{})
		require.NoError(t, err)
		defer dialog.Close()

		w, _ := dialog.AudioWriter()
		_, err = w.Write([]byte("1234"))
		require.NoError(t, err)

		buf := make([]byte, media.RTPBufSize)
		r, _ := dialog.AudioReader()
		r.Read(buf)
		require.NoError(t, err)

		t.Log("Hanguping")
		dialog.Hangup(ctx)
	}
}

func TestIntegrationBridgingMix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Create transaction users, as many as needed.
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("inbound"),
	)
	defer ua.Close()
	tu := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  5090,
		},
	))

	bridge := NewBridgeMix()
	bridgeServeWg := sync.WaitGroup{}
	err := tu.ServeBackground(ctx, func(in *DialogServerSession) {
		bridgeServeWg.Add(1)
		defer bridgeServeWg.Done()

		in.Trying()
		in.Ringing()
		in.Answer()

		// Add us in bridge
		t.Log("Adding into bridge", in.ID)
		if err := bridge.AddDialogSession(in); err != nil {
			t.Log("Adding dialog in bridge failed", err)
			return
		}
		defer func() {
			t.Log("Removing from bridge", in.ID)
			bridge.RemoveDialogSession(in.ID)
		}()

		// if err := bridge.Wait(); err != nil {
		// 	t.Log("Bridge wait exit with error", "error", err, "id", in.ID)
		// }
		<-in.Context().Done()
	})
	assert.NoError(t, err)

	t.Run("BridgeAddRemove", func(t *testing.T) {
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := newDialer(ua)

		dialogs := make([]*DialogClientSession, 3)
		for i := range 3 {
			t.Log("Inviting", "i", i)
			dialog, err := dg.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, InviteOptions{})
			require.NoError(t, err)
			dialogs[i] = dialog
		}
		// bridge.mu.Lock()
		// assert.Equal(t, 3, len(bridge.dialogs))
		// assert.Equal(t, 1, bridge.mixState)
		// bridge.mu.Unlock()

		for _, dialog := range dialogs {
			dialog.Hangup(ctx)
			dialog.Close()
		}

		bridgeServeWg.Wait()
		assert.Equal(t, 0, len(bridge.dialogs))
		assert.EqualValues(t, 0, bridge.stateRead())
	})

	t.Run("CheckMixing", func(t *testing.T) {

		mixedBuf := make([]byte, 12)
		audio.PCMMix(mixedBuf, mixedBuf, ulawDecode([]byte("123450")))
		audio.PCMMix(mixedBuf, mixedBuf, ulawDecode([]byte("123451")))
		audio.PCMUnmix(mixedBuf, mixedBuf, ulawDecode([]byte("123451")))
		t.Log("Mixed", ulawEncode(mixedBuf), string(ulawEncode(mixedBuf)))
	})

	t.Run("SoundMixed", func(t *testing.T) {
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := newDialer(ua)

		// Make number of calls that will have audio mixed in bridge
		wg := sync.WaitGroup{}
		bridge.WaitDialogsNum = 3 // Do not start mixing until all 3 get joined, otherwise there will be no gurantee when something is mixed
		for i := range 3 {
			dialog, err := dg.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, InviteOptions{})
			require.NoError(t, err)
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				defer dialog.Close()
				sound := []byte("12345" + strconv.Itoa(i))

				w, _ := dialog.AudioWriter()
				r, _ := dialog.AudioReader()
				_, err = w.Write(sound)
				require.NoError(t, err)

				buf := make([]byte, media.RTPBufSize)
				n, err := r.Read(buf)
				require.NoError(t, err)
				t.Log("Sound received", "i", i, "buf", buf[:n], "ssrc", dialog.RTPPacketWriter.SSRC)
				t.Log("Hanguping", "ssrc", dialog.RTPPacketWriter.SSRC)

				if i == 0 {
					assert.Equal(t, []byte{34, 35, 36, 37, 38, 34}, buf[:n]) // For now As regression
				} else if i == 1 {
					assert.Equal(t, []byte{34, 35, 36, 37, 38, 34}, buf[:n]) // For now As regression
				} else if i == 2 {
					assert.Equal(t, []byte{34, 35, 36, 37, 38, 33}, buf[:n]) // For now As regression
				}
				dialog.Hangup(ctx)
			}(i)

		}
		wg.Wait()
	})

	t.Run("SoundProxied", func(t *testing.T) {
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := newDialer(ua)

		// Make number of calls that will have audio mixed in bridge
		// wg := sync.WaitGroup{}
		bridge.WaitDialogsNum = 2 // Do not start mixing until all 3 get joined, otherwise there will be no gurantee when something is mixed

		dialog1, err := dg.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, InviteOptions{})
		require.NoError(t, err)
		defer dialog1.Hangup(ctx)

		dialog2, err := dg.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, InviteOptions{})
		require.NoError(t, err)
		defer dialog2.Hangup(ctx)

		// Write sound on dialog 1 and make sure it is read on dialog2
		sound := []byte("123450")
		go func(dialog *DialogClientSession) {
			w, _ := dialog.AudioWriter()
			for i := 0; i < 3; i++ {
				w.Write(sound)
			}

		}(dialog1)

		r, _ := dialog2.AudioReader()

		buf := make([]byte, media.RTPBufSize)
		for i := 0; i < 3; i++ {
			n, err := r.Read(buf)
			require.NoError(t, err)
			assert.Equal(t, sound, buf[:n])
			t.Log("Sound received", "buf", buf[:n])
		}
	})

	t.Run("SoundMixingLong", func(t *testing.T) {
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := newDialer(ua)

		// Make number of calls that will have audio mixed in bridge
		wg := sync.WaitGroup{}
		bridge.WaitDialogsNum = 3 // Do not start mixing until all 3 get joined, otherwise there will be no gurantee when something is mixed
		for i := range 3 {
			// Create some jitter
			time.Sleep(5 * time.Millisecond)
			dialog, err := dg.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, InviteOptions{})
			require.NoError(t, err)
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				defer dialog.Close()
				sound := []byte("12345" + strconv.Itoa(i))

				w, _ := dialog.AudioWriter()
				r, _ := dialog.AudioReader()

				for k := 0; k < 20; k++ {
					echoTime := time.Now()
					_, err = w.Write(sound)
					require.NoError(t, err)

					buf := make([]byte, media.RTPBufSize)
					n, err := r.Read(buf)
					require.NoError(t, err)

					assert.Equal(t, 6, n)
					// We expect that there will be some processing due to sample arriving
					assert.LessOrEqual(t, 15*time.Millisecond, time.Since(echoTime))
				}
				t.Log("Hanguping", "ssrc", dialog.RTPPacketWriter.SSRC)
				// If we stop fast we will prevent mixing and lose some packets
				// This currently is issue and maybe only buffering could solve IT
				time.Sleep(200 * time.Millisecond)
				dialog.Hangup(ctx)
			}(i)

		}
		wg.Wait()
	})

	t.Run("MixingWorksAfterLeaving", func(t *testing.T) {
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := newDialer(ua)

		// Make number of calls that will have audio mixed in bridge
		bridge.WaitDialogsNum = 2 // Do not start mixing until all 3 get joined, otherwise there will be no gurantee when something is mixed
		dialogs := make([]*DialogClientSession, 3)
		for i := range 3 {
			dialog, err := dg.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, InviteOptions{})
			require.NoError(t, err)
			dialogs[i] = dialog

			sound := []byte("12345" + strconv.Itoa(i))

			w, _ := dialog.AudioWriter()
			_, err = w.Write(sound)
			require.NoError(t, err)
		}

		require.Eventually(t, func() bool {
			bridge.mu.Lock()
			defer bridge.mu.Unlock()
			t.Log("Bridge dialogs", len(bridge.dialogs))
			return len(bridge.dialogs) == 3
		}, 3*time.Second, 100*time.Millisecond)

		// Lets have first leaving bridge
		dialog := dialogs[0]
		t.Log("Hanguping", dialog.ID)
		dialog.Hangup(dialog.Context())
		dialog.Close()
		dialogs = dialogs[1:]

		// We should wait that this complets somehow?
		require.Eventually(t, func() bool {
			bridge.mu.Lock()
			defer bridge.mu.Unlock()
			return len(bridge.dialogs) == 2 && bridge.stateRead() == 1
		}, 3*time.Second, 100*time.Millisecond)

		wg := sync.WaitGroup{}
		for i, dialog := range dialogs {
			wg.Add(1)
			go func(i int, dialog *DialogClientSession) {
				defer wg.Done()
				sound := []byte("12345" + strconv.Itoa(i))
				w, _ := dialog.AudioWriter()
				r, _ := dialog.AudioReader()
				_, err := w.Write(sound)
				require.NoError(t, err)
				buf := make([]byte, media.RTPBufSize)
				n, err := r.Read(buf)
				require.NoError(t, err)
				t.Log("Sound received", "i", i, "buf", buf[:n], "ssrc", dialog.RTPPacketWriter.SSRC)
			}(i, dialog)
		}
		// Wait all to read
		wg.Wait()

		// Now hangup
		for _, dialog := range dialogs[1:] {
			dialog.Hangup(dialog.Context())
			dialog.Close()
		}
	})
}
