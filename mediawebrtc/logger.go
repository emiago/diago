package mediawebrtc

import (
	"log/slog"

	"github.com/pion/webrtc/v3"
)

var (
	defLogger *slog.Logger
)

// SetDefaultLogger sets default logger that will be used withing sip package
// Must be called before any usage of library
func SetDefaultLogger(l *slog.Logger) {
	defLogger = l
}

func DefaultLogger() *slog.Logger {
	if defLogger != nil {
		return defLogger
	}
	return slog.Default()
}

func logICECandidatePairs(log *slog.Logger, rtpSender *webrtc.RTPSender) {
	// Find out ICE pairs
	dt := rtpSender.Transport()
	if dt != nil {
		ices := dt.ICETransport()
		if ices != nil {
			pair, err := ices.GetSelectedCandidatePair()
			if err == nil && pair != nil {
				log.Debug("ICE Selected candidate pair",
					"localAddr", pair.Local.Address,
					"localPort", pair.Local.Port,
					"localType", pair.Local.Typ.String(),
					"remoteAddr", pair.Remote.Address,
					"remotePort", pair.Remote.Port,
					"remoteType", pair.Remote.Typ.String(),
				)
			}
		}
	}
}
