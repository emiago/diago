package audio

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rtpBuffer struct {
	buf []rtp.Packet
}

func (b *rtpBuffer) WriteRTP(p *rtp.Packet) error {
	b.buf = append(b.buf, *p)
	return nil
}

func TestMonitorPCMReaderWriter(t *testing.T) {
	codecR := media.CodecAudioAlaw

	audioAlawBuf := make([]byte, 4*160)
	_, err := EncodeAlawTo(audioAlawBuf, bytes.Repeat([]byte("0123456789"), media.CodecAudioAlaw.Samples16()*4/10))
	require.NoError(t, err)

	t.Run("Reader", func(t *testing.T) {
		rtpBufferReader := bytes.NewBuffer(audioAlawBuf)

		recording := bytes.NewBuffer([]byte{})
		mon := &MonitorPCMReader{}
		mon.Init(recording, codecR, rtpBufferReader)

		mon.Read(make([]byte, 160))
		mon.Read(make([]byte, 160))
		time.Sleep(3 * codecR.SampleDur) // 1 now comming and 2 delayed
		_, err = media.ReadAll(mon, 160)
		require.NoError(t, err)

		mon.Flush()

		// 2 Frames, 2 Silence, 2 Frames
		frameSize := codecR.Samples16()
		assert.Equal(t, 2*frameSize+2*frameSize+2*frameSize, recording.Len())
	})

	t.Run("Writer", func(t *testing.T) {
		// Lets
		recording := bytes.NewBuffer([]byte{})
		mon := &MonitorPCMWriter{}
		mon.Init(recording, codecR, bytes.NewBuffer([]byte{}))

		mon.Write(audioAlawBuf[:160])
		mon.Write(audioAlawBuf[160 : 2*160])
		time.Sleep(3 * codecR.SampleDur) // 1 now comming and 2 delayed
		_, err = media.WriteAll(mon, audioAlawBuf[2*160:], 160)
		require.NoError(t, err)

		mon.Flush()

		// 2 Frames, 2 Silence, 2 Frames
		frameSize := codecR.Samples16()
		assert.Equal(t, 2*frameSize+2*frameSize+2*frameSize, recording.Len())
	})

}

func TestMonitorPCMStereo(t *testing.T) {
	audioAlawBuf := make([]byte, 4*160)
	_, err := EncodeAlawTo(audioAlawBuf, bytes.Repeat([]byte("0123456789"), media.CodecAudioAlaw.Samples16()*4/10))
	require.NoError(t, err)
	audioPCMBuf := make([]byte, 4*320)
	DecodeAlawTo(audioPCMBuf, audioAlawBuf)

	t.Run("SmallData", func(t *testing.T) {
		mon := &MonitorPCMStereo{}
		recording := bytes.NewBuffer([]byte{})
		err = mon.Init(recording, media.CodecAudioAlaw, bytes.NewBuffer(audioAlawBuf), bytes.NewBuffer([]byte{}))
		require.NoError(t, err)

		errWrite := make(chan error)
		go func() {
			_, err = media.WriteAll(mon, audioAlawBuf, 160)
			errWrite <- err
		}()

		_, err = media.ReadAll(mon, 160)
		require.NoError(t, err)

		err = <-errWrite
		require.NoError(t, err)

		err = mon.Close()
		require.NoError(t, err)

		// Do files get removed
		_, err = os.Stat(mon.PCMFileRead.Name())
		assert.True(t, os.IsNotExist(err))
		_, err = os.Stat(mon.PCMFileWrite.Name())
		assert.True(t, os.IsNotExist(err))

		frameSize := media.CodecAudioAlaw.Samples16()
		assert.Equal(t, 8*frameSize, recording.Len())
		// Check does data alternate
		stereo := recording.Bytes()
		assert.Equal(t, audioPCMBuf[:2], stereo[:2])
		assert.Equal(t, audioPCMBuf[:2], stereo[2:4])
	})

	t.Run("BigData", func(t *testing.T) {
		audioAlawBufBig := bytes.Repeat(audioAlawBuf, 20)

		mon := &MonitorPCMStereo{}
		recording := bytes.NewBuffer([]byte{})
		err = mon.Init(recording, media.CodecAudioAlaw, bytes.NewBuffer(audioAlawBufBig), bytes.NewBuffer([]byte{}))
		require.NoError(t, err)

		errWrite := make(chan error)
		go func() {
			_, err = media.WriteAll(mon, audioAlawBufBig, 160)
			errWrite <- err
		}()

		_, err = media.ReadAll(mon, 160)
		require.NoError(t, err)

		err = <-errWrite
		require.NoError(t, err)

		err = mon.Close()
		require.NoError(t, err)

		frameSize := media.CodecAudioAlaw.Samples16()
		// 80 frames * 2 channels
		assert.Equal(t, 80*2*frameSize, recording.Len())
	})

}
