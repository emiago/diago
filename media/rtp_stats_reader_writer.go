package media

import (
	"io"
)

type OnRTPReadStats func(stats RTPReadStats)
type OnRTPWriteStats func(stats RTPWriteStats)

type RTPStatsReader struct {
	// Reader should be your AudioReade or any other interceptor RTP reader that is reading audio stream
	Reader     io.Reader
	RTPSession *RTPSession
	// OnRTPReadStats is fired each time on Read RTP. Must not block
	OnRTPReadStats OnRTPReadStats
}

func (i *RTPStatsReader) Read(b []byte) (int, error) {
	n, err := i.Reader.Read(b)
	if err != nil {
		return n, err
	}

	stats := i.RTPSession.ReadStats()
	i.OnRTPReadStats(stats)
	return n, err
}

type RTPStatsWriter struct {
	// Writer should be your Writer or any other interceptor RTP writer that is reading audio stream
	Writer     io.Writer
	RTPSession *RTPSession
	// ONRTPWriteStats is fired each time on Read RTP. Must not block
	OnRTPWriteStats OnRTPWriteStats
}

func (i *RTPStatsWriter) Write(b []byte) (int, error) {
	n, err := i.Writer.Write(b)
	if err != nil {
		return n, err
	}

	stats := i.RTPSession.WriteStats()
	i.OnRTPWriteStats(stats)
	return n, err
}
