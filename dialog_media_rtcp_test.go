package diago

import (
	"net"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/rtcp"
)

// helper: создает связанную пару MediaSession (локальную и удалённую) на loopback, чтобы слать RTCP локально
func makePairedSessions(t *testing.T) (*media.MediaSession, *media.MediaSession) {
	t.Helper()

	local, err := media.NewMediaSession(net.ParseIP("127.0.0.1"), 0)
	if err != nil {
		t.Fatalf("local NewMediaSession: %v", err)
	}

	remote, err := media.NewMediaSession(net.ParseIP("127.0.0.1"), 0)
	if err != nil {
		local.Close()
		t.Fatalf("remote NewMediaSession: %v", err)
	}

	// свяжем адреса друг с другом
	local.SetRemoteAddr(&net.UDPAddr{IP: remote.Laddr.IP, Port: remote.Laddr.Port})
	remote.SetRemoteAddr(&net.UDPAddr{IP: local.Laddr.IP, Port: local.Laddr.Port})

	return local, remote
}

// Тест: хук OnRTCP вызывается при получении RTCP SenderReport/ReceiverReport
func TestDialogMedia_OnRTCP_CallbackInvoked(t *testing.T) {
	local, remote := makePairedSessions(t)
	defer local.Close()
	defer remote.Close()

	// Оборачиваем локальную сессию в RTPSession и DialogMedia
	rtpSess := media.NewRTPSession(local)
	if err := rtpSess.MonitorBackground(); err != nil {
		t.Fatalf("MonitorBackground: %v", err)
	}
	defer rtpSess.Close()

	dm := &DialogMedia{}
	dm.initRTPSessionUnsafe(local, rtpSess)

	ch := make(chan rtcp.Packet, 1)
	dm.OnRTCP(func(pkt rtcp.Packet) {
		select {
		case ch <- pkt:
		default:
		}
	})

	// Отправим с удалённой стороны простой ReceiverReport на локальный RTCP порт
	rr := &rtcp.ReceiverReport{SSRC: 0}
	if err := remote.WriteRTCP(rr); err != nil {
		t.Fatalf("remote WriteRTCP: %v", err)
	}

	select {
	case <-ch:
		// ок
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout: RTCP callback not invoked")
	}
}

// Тест: неблокирующий колбэк не зависает MonitorBackground
func TestDialogMedia_OnRTCP_NonBlocking(t *testing.T) {
	local, remote := makePairedSessions(t)
	defer local.Close()
	defer remote.Close()

	rtpSess := media.NewRTPSession(local)
	if err := rtpSess.MonitorBackground(); err != nil {
		t.Fatalf("MonitorBackground: %v", err)
	}
	defer rtpSess.Close()

	dm := &DialogMedia{}
	dm.initRTPSessionUnsafe(local, rtpSess)

	done := make(chan struct{})
	dm.OnRTCP(func(pkt rtcp.Packet) {
		// эмулируем «тяжёлый» обработчик
		time.Sleep(300 * time.Millisecond)
		close(done)
	})

	if err := remote.WriteRTCP(&rtcp.ReceiverReport{SSRC: 0}); err != nil {
		t.Fatalf("remote WriteRTCP: %v", err)
	}

	select {
	case <-done:
		// ок — обработчик завершился, поток не завис
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout: non-blocking handler seems blocked")
	}
}

// Тест: отложенная регистрация — если OnRTCP вызван до создания RTPSession,
// хук привяжется при обновлении медиа и получит пакет
func TestDialogMedia_OnRTCP_DeferredRegistration(t *testing.T) {
	// подготовим будущую локальную медиасессию, но RTPSession ещё не создаём
	ms, err := media.NewMediaSession(net.ParseIP("127.0.0.1"), 0)
	if err != nil {
		t.Fatalf("NewMediaSession: %v", err)
	}
	defer ms.Close()

	// удалённая сторона
	rm, err := media.NewMediaSession(net.ParseIP("127.0.0.1"), 0)
	if err != nil {
		t.Fatalf("remote NewMediaSession: %v", err)
	}
	defer rm.Close()

	ms.SetRemoteAddr(&net.UDPAddr{IP: rm.Laddr.IP, Port: rm.Laddr.Port})
	rm.SetRemoteAddr(&net.UDPAddr{IP: ms.Laddr.IP, Port: ms.Laddr.Port})

	dm := &DialogMedia{}
	// имитируем состояние до установления RTPSession
	dm.initMediaSessionUnsafe(ms, nil, nil)

	got := make(chan struct{}, 1)
	dm.OnRTCP(func(pkt rtcp.Packet) { // регистрируем раньше
		select {
		case got <- struct{}{}:
		default:
		}
	})

	// теперь создаём RTPSession и триггерим onMediaUpdate через sdpUpdateUnsafe-подобный путь
	rtpSess := media.NewRTPSession(ms)
	if err := rtpSess.MonitorBackground(); err != nil {
		t.Fatalf("MonitorBackground: %v", err)
	}
	defer rtpSess.Close()

	// свяжем DialogMedia с новой RTPSession (как делает sdpUpdateUnsafe)
	dm.initRTPSessionUnsafe(ms, rtpSess)

	// вручную вызовем onMediaUpdate, как делает sdpReInviteUnsafe после обновления ссылок
	if dm.onMediaUpdate != nil {
		dm.onMediaUpdate(dm)
	}

	// отправим RTCP
	if err := rm.WriteRTCP(&rtcp.ReceiverReport{SSRC: 0}); err != nil {
		t.Fatalf("remote WriteRTCP: %v", err)
	}

	select {
	case <-got:
		// ок
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout: deferred OnRTCP not invoked")
	}
}
