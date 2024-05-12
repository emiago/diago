package diago

import (
	"github.com/emiago/diago/audio"
)

type PlaybackControl struct {
	Playback

	control *audio.PlaybackControl
}

func (p *PlaybackControl) Mute(mute bool) {
	p.control.Mute(mute)
}

func (p *PlaybackControl) Stop() {
	p.control.Stop()
}
