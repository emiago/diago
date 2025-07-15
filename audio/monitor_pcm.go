package audio

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"time"

	"github.com/emiago/diago/media"
	"github.com/google/uuid"
)

var (
	RecordingFlushSize = 4096
)

type MonitorPCMReader struct {
	audioReader io.Reader
	writer      *bufio.Writer // Lets use Buffered flushing

	codec    media.Codec
	decoder  PCMDecoderBuffer
	silence  []byte
	lastTime time.Time
}

func (m *MonitorPCMReader) Init(w io.Writer, codec media.Codec, audioReader io.Reader) error {
	bw := bufio.NewWriterSize(w, RecordingFlushSize)
	m.writer = bw
	m.codec = codec
	m.audioReader = audioReader

	decoder := PCMDecoderBuffer{}
	if err := decoder.Init(codec); err != nil {
		return err
	}
	m.decoder = decoder

	samples16 := codec.Samples16()
	silence := bytes.Repeat([]byte{0}, samples16) // This alloc could be avoided
	m.silence = silence
	return nil
}

func (m *MonitorPCMReader) Flush() error {
	return m.writer.Flush()
}

// Monitoring starts with first packet arrived, but you can shift with start time. Ex stream are not continious
func (m *MonitorPCMReader) StartTime(t time.Time) {
	m.lastTime = t
}

func (m *MonitorPCMReader) Read(b []byte) (int, error) {
	n, err := m.audioReader.Read(b)
	if err != nil {
		return n, err
	}
	// Check do we need to inject silence
	now := time.Now()
	if !m.lastTime.IsZero() {
		diff := uint32(now.Sub(m.lastTime).Seconds() * float64(m.codec.SampleRate))
		srt := m.codec.SampleTimestamp()
		for i := 2 * srt; i < diff; i += srt {
			if _, err := m.writer.Write(m.silence); err != nil {
				return n, err
			}
		}
	}
	m.lastTime = now

	// Decode stream to PCM unless stream is already decoded?
	if _, err := m.decoder.Write(b[:n]); err != nil {
		return 0, err
	}
	lpcm := m.decoder.ReadFull()

	// Write to outer stream. Expecting some buffer with flushing will happen
	_, err = m.writer.Write(lpcm)
	return n, err
}

type MonitorPCMWriter struct {
	audioWriter io.Writer
	writer      *bufio.Writer // Lets use Buffered flushing

	codec    media.Codec
	decoder  PCMDecoderBuffer
	silence  []byte
	lastTime time.Time
}

func (m *MonitorPCMWriter) Init(w io.Writer, codec media.Codec, audioWriter io.Writer) error {
	bw := bufio.NewWriterSize(w, RecordingFlushSize)
	m.writer = bw
	m.codec = codec
	m.audioWriter = audioWriter

	decoder := PCMDecoderBuffer{}
	if err := decoder.Init(codec); err != nil {
		return err
	}
	m.decoder = decoder

	samples16 := codec.Samples16()
	silence := bytes.Repeat([]byte{0}, samples16) // This alloc could be avoided
	m.silence = silence
	return nil
}

func (m *MonitorPCMWriter) Flush() error {
	return m.writer.Flush()
}

func (m *MonitorPCMWriter) Write(b []byte) (int, error) {
	// Check do we need to inject silence
	now := time.Now()
	if !m.lastTime.IsZero() {
		diff := uint32(now.Sub(m.lastTime).Seconds() * float64(m.codec.SampleRate))
		srt := m.codec.SampleTimestamp()
		for i := 2 * srt; i < diff; i += srt {
			if _, err := m.writer.Write(m.silence); err != nil {
				return 0, err
			}
		}
	}
	m.lastTime = now

	n, err := m.audioWriter.Write(b)
	if err != nil {
		return n, err
	}

	// Decode stream to PCM unless stream is already decoded?
	if _, err := m.decoder.Write(b[:n]); err != nil {
		return 0, err
	}
	lpcm := m.decoder.ReadFull()

	// Write to outer stream. Expecting some buffer with flushing will happen
	_, err = m.writer.Write(lpcm)
	return n, err
}

type MonitorPCMStereo struct {
	MonitorPCMReader
	MonitorPCMWriter

	PCMFileRead  *os.File
	PCMFileWrite *os.File

	recording io.Writer
}

// It supports only single codec, which must be same for reader and writer
func (m *MonitorPCMStereo) Init(record io.Writer, codec media.Codec, audioReader io.Reader, audioWriter io.Writer) error {
	m.recording = record

	uuid := uuid.New().String()
	var err error
	err = func() error {
		if m.PCMFileRead == nil {
			filepath := path.Join(os.TempDir(), uuid+"_monitor_reader.raw")
			m.PCMFileRead, err = os.OpenFile(filepath, os.O_CREATE|os.O_RDWR, 0755)
			if err != nil {
				return err
			}
		}

		if m.PCMFileWrite == nil {
			filepath := path.Join(os.TempDir(), uuid+"_monitor_writer.raw")
			m.PCMFileWrite, err = os.OpenFile(filepath, os.O_CREATE|os.O_RDWR, 0755)
			if err != nil {
				return err
			}
		}

		if err := m.MonitorPCMReader.Init(m.PCMFileRead, codec, audioReader); err != nil {
			return err
		}

		if err := m.MonitorPCMWriter.Init(m.PCMFileWrite, codec, audioWriter); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		return errors.Join(err, m.removeTmpFiles())
	}

	return nil
}

func (m *MonitorPCMStereo) removeTmpFiles() (err error) {
	if m.PCMFileRead != nil {
		e1 := m.PCMFileRead.Close()
		e2 := os.Remove(m.PCMFileRead.Name())
		err = errors.Join(err, e1, e2)
	}

	if m.PCMFileWrite != nil {
		e1 := m.PCMFileWrite.Close()
		e2 := os.Remove(m.PCMFileWrite.Name())
		err = errors.Join(err, e1, e2)
	}
	return err
}

func (m *MonitorPCMStereo) Close() error {
	if err := m.Flush(); err != nil {
		return err
	}
	if err := m.interleave(); err != nil {
		return err
	}

	return m.removeTmpFiles()
}

func (m *MonitorPCMStereo) Flush() error {
	if err := m.MonitorPCMReader.Flush(); err != nil {
		return err
	}
	if err := m.MonitorPCMWriter.Flush(); err != nil {
		return err
	}
	return nil
}

func (m *MonitorPCMStereo) interleave() error {
	fr := m.PCMFileRead
	fw := m.PCMFileWrite
	recording := m.recording
	if _, err := fr.Seek(0, 0); err != nil {
		return err
	}
	if _, err := fw.Seek(0, 0); err != nil {
		return err
	}

	// Read frames from both files and interleave
	readBuf1 := make([]byte, RecordingFlushSize/2)
	readBuf2 := make([]byte, RecordingFlushSize/2)
	stereoBuf := make([]byte, (RecordingFlushSize/2)*2)
	size := 2 // 16 bit
	for {
		n1, err1 := io.ReadFull(fr, readBuf1)
		n2, err2 := io.ReadFull(fw, readBuf2)

		n := max(n1, n2)

		if (err1 != nil || err2 != nil) && n == 0 {
			if !errors.Is(err1, io.EOF) {
				return err1
			}

			if !errors.Is(err2, io.EOF) {
				return err2
			}
			break
		}

		// interleave
		copyN := 0
		for i, j := 0, 0; i < n; i += size {
			copyN += copy(stereoBuf[j:j+size], readBuf1[i:i+size])
			copyN += copy(stereoBuf[j+size:j+2*size], readBuf2[i:i+size])
			j += 2 * size // 2 channels * size
		}

		if _, err := recording.Write(stereoBuf[:copyN]); err != nil {
			return err
		}

	}

	return nil
}
