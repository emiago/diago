// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

type MediaStreamer interface {
	MediaStream(s *MediaSession) error
}

// TODO buid basic handling of media session
// - logger
// - mic
// - speaker
