// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package media

type MediaStreamer interface {
	MediaStream(s *MediaSession) error
}

// TODO buid basic handling of media session
// - logger
// - mic
// - speaker
