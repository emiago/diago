package diago

import "github.com/emiago/diago/audio"

type MediaStream struct {
}

// With control stream audio can be muted or unmuted
func NewControlStream(m *DialogMedia) *audio.PlaybackControl {
	playback := audio.NewPlaybackControl(m.RTPReader, m.RTPWriter)
	return playback
}
