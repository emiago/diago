package diago

// AddDialogWebrtcPion adds media from the Pion-backed WebRTC dialog stack.
func (b *Bridge) AddDialogWebrtcPion(m *DialogWebrtcPion) error {
	med := BridgeAudioMedia{}
	var err error
	med.Reader, err = m.AudioReader(WithAudioReaderWebrtcPionProps(&med.ReaderProps))
	if err != nil {
		return err
	}
	med.Writer, err = m.AudioWriter(WithAudioWriterWebrtcPionProps(&med.WriterProps))
	if err != nil {
		return err
	}

	if b.DTMFpass {
		dtmfReader := DTMFReader{}
		med.Reader, err = m.AudioReader(WithAudioReaderWebrtcPionProps(&med.ReaderProps), WithAudioReaderWebrtcPionDTMF(&dtmfReader))
		if err != nil {
			return err
		}
		dtmfWriter := DTMFWriter{}
		med.Writer, err = m.AudioWriter(WithAudioWriterWebrtcPionProps(&med.WriterProps), WithAudioWriterWebrtcPionDTMF(&dtmfWriter))
		if err != nil {
			return err
		}

		dtmfReader.OnDTMF(func(dtmf rune) error {
			return dtmfWriter.WriteDTMF(dtmf)
		})
	}
	return b.AddAudioMedia(&med)
}
