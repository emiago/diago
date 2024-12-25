// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Run command
// GOMAXPROCS=20 TEST_INTEGRATION=1 go test -bench=BenchmarkIntegrationClientServer -run $^ -benchmem -v . -benchtime=50x
func BenchmarkIntegrationClientServer(t *testing.B) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Use TEST_INTEGRATION env value to run this test")
		return
	}

	testCases := []struct {
		transport  string
		serverAddr string
		encrypted  bool
	}{
		{transport: "udp", serverAddr: "127.1.1.100:5060"},
		// {transport: "tcp", serverAddr: "127.1.1.100:5060"},
		// {transport: "ws", serverAddr: "127.1.1.100:5061"},
		// {transport: "tls", serverAddr: "127.1.1.100:5062", encrypted: true},
		// {transport: "wss", serverAddr: "127.1.1.100:5063", encrypted: true},
	}

	ctx, shutdown := context.WithCancel(context.Background())
	wg := sync.WaitGroup{}
	t.Cleanup(func() {
		shutdown()
		wg.Wait()
	})

	tran := Transport{
		Transport: "udp",
		BindHost:  "127.1.1.100",
		BindPort:  5060,
	}
	ua, _ := sipgo.NewUA()
	defer ua.Close()
	srv := NewDiago(ua, WithTransport(tran))

	err := srv.ServeBackground(ctx, func(d *DialogServerSession) {
		wg.Add(1)
		defer wg.Done()

		ctx := d.Context()

		err := d.Answer()
		if err != nil {
			t.Log(err.Error())
			return
		}

		pb, err := d.PlaybackCreate()
		if err != nil {
			t.Log(err.Error())
			return
		}
		_, err = pb.PlayFile("./testdata/files/demo-echodone.wav")
		if err != nil {
			t.Log(err.Error())
			return
		}

		err = d.Hangup(ctx)
		if err != nil {
			t.Log(err.Error())
		}
	})
	require.NoError(t, err)

	var maxInvitesPerSec chan struct{}
	maxInvitesPerSec = make(chan struct{}, 100)
	if v := os.Getenv("MAX_REQUESTS"); v != "" {
		t.Logf("Limiting number of requests: %s req/s", v)
		maxInvites, _ := strconv.Atoi(v)
		maxInvitesPerSec = make(chan struct{}, maxInvites)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(1 * time.Second):
					for i := 0; i < maxInvites; i++ {
						<-maxInvitesPerSec
					}
				}
			}
		}()
	}

	for _, tc := range testCases {
		t.Run(tc.transport, func(t *testing.B) {
			t.ResetTimer()
			t.ReportAllocs()

			t.RunParallel(func(p *testing.PB) {
				// Build UAC
				// ua, _ := NewUA(WithUserAgenTLSConfig(clientTLS))
				ua, _ := sipgo.NewUA()
				defer ua.Close()
				// client, err := sipgo.NewClient(ua)
				// require.NoError(t, err)
				phone := NewDiago(ua)
				id, _ := rand.Int(rand.Reader, big.NewInt(int64(runtime.GOMAXPROCS(0))))
				for p.Next() {
					t.Log("Making call goroutine=", id)
					// If we are running in limit mode
					if maxInvitesPerSec != nil {
						maxInvitesPerSec <- struct{}{}
					}
					dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					dialog, err := phone.Invite(dialCtx, sip.Uri{Host: tran.BindHost, Port: tran.BindPort, User: "dialer"}, InviteOptions{})
					cancel()

					require.NoError(t, err)

					mediapkts := 0
					outoforder := 0
					wg.Add(1)
					go func() {
						defer wg.Done()

						buf := make([]byte, media.RTPBufSize)
						var prevSeq uint16
						reader := dialog.mediaSession
						for {
							p := rtp.Packet{}
							_, err := reader.ReadRTP(buf, &p)
							if err != nil {
								return
							}
							if prevSeq > 0 && p.SequenceNumber != prevSeq+1 {
								outoforder++
							}
							mediapkts++
							prevSeq = p.SequenceNumber
						}
					}()

					start := time.Now()
					select {
					// Audio is 2 sec long
					case <-time.After(2 * time.Second):
						t.Log("NON SERVER hanguping")
						dialog.Hangup(context.TODO())
					case <-dialog.Context().Done():
					}
					callDuration := time.Since(start)
					dialog.Close()
					assert.Empty(t, outoforder, "Out of order media detected")
					assert.Greater(t, mediapkts, int(callDuration/(20*time.Millisecond))-10, "Not enough received packets")
				}
			})

			t.ReportMetric(float64(t.N)/t.Elapsed().Seconds(), "req/s")

			// ua, _ := NewUA(WithUserAgenTLSConfig(clientTLS))
			// 		client, err := NewClient(ua)
			// 		require.NoError(t, err)

			// for i := 0; i < t.N; i++ {
			// 	req, _, _ := createTestInvite(t, proto+":bob@"+tc.serverAddr, tc.transport, client.ip.String())
			// 	tx, err := client.TransactionRequest(ctx, req)
			// 	require.NoError(t, err)

			// 	res := <-tx.Responses()
			// 	assert.Equal(t, sip.StatusCode(200), res.StatusCode)

			// 	tx.Terminate()
			// }
			// t.ReportMetric(float64(t.N)/max(t.Elapsed().Seconds(), 1), "req/s")
		})
	}
}

func createTestInvite(t testing.TB, targetSipUri string, transport, addr string) (*sip.Request, string, string) {
	branch := sip.GenerateBranch()
	callid := "gotest-" + time.Now().Format(time.RFC3339Nano)
	ftag := fmt.Sprintf("%d", time.Now().UnixNano())
	return testCreateMessage(t, []string{
		"INVITE " + targetSipUri + " SIP/2.0",
		"Via: SIP/2.0/" + transport + " " + addr + ";branch=" + branch,
		"From: \"Alice\" <sip:alice@" + addr + ">;tag=" + ftag,
		"To: \"Bob\" <" + targetSipUri + ">",
		"Call-ID: " + callid,
		"CSeq: 1 INVITE",
		"Content-Length: 0",
		"",
		"",
	}).(*sip.Request), callid, ftag
}

func testCreateMessage(t testing.TB, rawMsg []string) sip.Message {
	msg, err := sip.ParseMessage([]byte(strings.Join(rawMsg, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}
