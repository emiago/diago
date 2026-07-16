// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/stun/v3"
)

var (
	// ICEGatherTimeout caps how long candidate gathering may block before the
	// agent proceeds with whatever it has collected.
	ICEGatherTimeout = 5 * time.Second

	// ICEConnectTimeout caps how long connectivity checks may run before the
	// session gives up on nominating a candidate pair.
	ICEConnectTimeout = 30 * time.Second
)

// ICEConfig enables ICE (RFC 8445) on a media session. It is consumed by
// MediaSession when set on ICEConf, and it is only honoured together with
// SecureRTP = SecureRTPModeDTLS, which is the WebRTC profile.
type ICEConfig struct {
	// STUNServers gathers server reflexive candidates.
	// Example: []string{"stun:stun.l.google.com:19302"}
	STUNServers []string

	// Lite runs an ICE lite agent, which only answers connectivity checks and
	// never initiates them. Suitable for an endpoint on a public address.
	Lite bool

	// NetworkTypes restricts candidate gathering. Defaults to udp4.
	NetworkTypes []ice.NetworkType
}

// ICEAgent wraps a pion ICE agent and drives it from SDP attributes.
//
// The agent never owns its own socket: Init takes the session socket and
// wraps it in a UDP mux, so the host candidate port and the SDP m=audio port
// are the same port. That property is what allows a single ICE connection to
// carry DTLS, RTP and RTCP, see iceMux.
type ICEAgent struct {
	agent  *ice.Agent
	config ICEConfig

	ufrag string
	pwd   string

	remoteUfrag string
	remotePwd   string

	candidates []ice.Candidate

	// udpMux wraps the caller supplied socket. Closing it closes that socket.
	udpMux ice.UDPMux
	conn   *ice.Conn
}

// NewICEAgent builds an agent and its local credentials. No socket is bound
// and no candidate is gathered until Init.
func NewICEAgent(config ICEConfig) (*ICEAgent, error) {
	if len(config.NetworkTypes) == 0 {
		config.NetworkTypes = []ice.NetworkType{ice.NetworkTypeUDP4}
	}

	ufrag, err := generateICEString(16)
	if err != nil {
		return nil, fmt.Errorf("ice: generate ufrag: %w", err)
	}
	pwd, err := generateICEString(22)
	if err != nil {
		return nil, fmt.Errorf("ice: generate pwd: %w", err)
	}

	return &ICEAgent{
		ufrag:  ufrag,
		pwd:    pwd,
		config: config,
	}, nil
}

// Init wraps conn in a UDP mux, creates the pion agent and gathers candidates.
// conn stays owned by the agent from this point: Close closes it.
func (a *ICEAgent) Init(ctx context.Context, conn *net.UDPConn) error {
	if conn == nil {
		return fmt.Errorf("ice: conn must be set")
	}

	mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: conn})
	a.udpMux = mux

	agent, err := ice.NewAgent(&ice.AgentConfig{
		NetworkTypes: a.config.NetworkTypes,
		Urls:         iceSTUNURIs(a.config.STUNServers),
		UDPMux:       mux,
		Lite:         a.config.Lite,
		LocalUfrag:   a.ufrag,
		LocalPwd:     a.pwd,
	})
	if err != nil {
		_ = mux.Close()
		a.udpMux = nil
		return fmt.Errorf("ice: new agent: %w", err)
	}
	a.agent = agent

	if err := a.gatherCandidates(ctx); err != nil {
		return fmt.Errorf("ice: gather candidates: %w", err)
	}
	return nil
}

// iceSTUNURIs parses the STUN server list. An unparsable entry is logged and
// skipped rather than failing the session: ICE still works on host candidates.
func iceSTUNURIs(stunServers []string) []*stun.URI {
	urls := make([]*stun.URI, 0, len(stunServers))
	for _, s := range stunServers {
		url, err := stun.ParseURI(s)
		if err != nil {
			DefaultLogger().Warn("Skipping bad STUN server URL", "url", s, "error", err)
			continue
		}
		urls = append(urls, url)
	}
	return urls
}

// gatherCandidates blocks until pion reports gathering complete, the gather
// timeout fires or ctx is done. A timeout is not an error, the agent proceeds
// with the candidates it has, which always includes the host candidate.
func (a *ICEAgent) gatherCandidates(ctx context.Context) error {
	candidateCh := make(chan ice.Candidate, 10)
	if err := a.agent.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			// nil candidate marks gathering complete
			close(candidateCh)
			return
		}
		candidateCh <- c
	}); err != nil {
		return fmt.Errorf("on candidate: %w", err)
	}

	if err := a.agent.GatherCandidates(); err != nil {
		return fmt.Errorf("gather: %w", err)
	}

	timeout := time.After(ICEGatherTimeout)
	a.candidates = make([]ice.Candidate, 0, 5)
	for {
		select {
		case c, ok := <-candidateCh:
			if !ok {
				DefaultLogger().Debug("ICE gathering completed", "candidates", len(a.candidates))
				return nil
			}
			a.candidates = append(a.candidates, c)
			DefaultLogger().Debug("ICE candidate gathered",
				"type", c.Type().String(),
				"address", c.Address(),
				"port", c.Port(),
			)
		case <-timeout:
			DefaultLogger().Warn("ICE gathering timed out", "candidates", len(a.candidates))
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// SetRemoteCredentials sets the ufrag and pwd read from the remote SDP.
func (a *ICEAgent) SetRemoteCredentials(ufrag, pwd string) {
	a.remoteUfrag = ufrag
	a.remotePwd = pwd
}

// AddRemoteCandidate adds one remote candidate read from an SDP a=candidate
// attribute. The value must not carry the "candidate:" prefix.
func (a *ICEAgent) AddRemoteCandidate(candidate string) error {
	if a.agent == nil {
		return fmt.Errorf("ice: agent not initialized")
	}
	c, err := ice.UnmarshalCandidate(candidate)
	if err != nil {
		return fmt.Errorf("ice: parse candidate %q: %w", candidate, err)
	}
	if err := a.agent.AddRemoteCandidate(c); err != nil {
		return fmt.Errorf("ice: add remote candidate: %w", err)
	}
	return nil
}

// Connect runs connectivity checks and returns the selected pair connection
// plus the remote address it settled on. The offerer is the controlling agent
// per RFC 8445 section 6.1. It blocks until a pair is nominated or ctx is done.
func (a *ICEAgent) Connect(ctx context.Context, controlling bool) (*ice.Conn, *net.UDPAddr, error) {
	if a.agent == nil {
		return nil, nil, fmt.Errorf("ice: agent not initialized")
	}
	if a.remoteUfrag == "" || a.remotePwd == "" {
		return nil, nil, fmt.Errorf("ice: remote credentials not set")
	}

	var conn *ice.Conn
	var err error
	if controlling {
		conn, err = a.agent.Dial(ctx, a.remoteUfrag, a.remotePwd)
	} else {
		conn, err = a.agent.Accept(ctx, a.remoteUfrag, a.remotePwd)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("ice: connectivity checks failed: %w", err)
	}
	a.conn = conn

	pair, err := a.agent.GetSelectedCandidatePair()
	if err != nil {
		return nil, nil, fmt.Errorf("ice: selected pair: %w", err)
	}
	if pair == nil {
		return nil, nil, fmt.Errorf("ice: no candidate pair selected")
	}

	raddr := &net.UDPAddr{
		IP:   net.ParseIP(pair.Remote.Address()),
		Port: int(pair.Remote.Port()),
	}
	DefaultLogger().Debug("ICE connected",
		"controlling", controlling,
		"local", pair.Local.String(),
		"remote", pair.Remote.String(),
	)
	return conn, raddr, nil
}

// Credentials returns the local ufrag and pwd for SDP generation.
func (a *ICEAgent) Credentials() (ufrag, pwd string) {
	return a.ufrag, a.pwd
}

// Candidates returns the gathered local candidates for SDP generation.
func (a *ICEAgent) Candidates() []ice.Candidate {
	return a.candidates
}

// Close releases the agent, the UDP mux and with it the session socket the mux
// wraps. The ICE connection is closed by the agent, closing it here as well
// would race with pion internals, so it is only dropped.
func (a *ICEAgent) Close() error {
	var errs []error
	if a.agent != nil {
		if err := a.agent.Close(); err != nil {
			errs = append(errs, err)
		}
		a.agent = nil
	}
	if a.udpMux != nil {
		if err := a.udpMux.Close(); err != nil {
			errs = append(errs, err)
		}
		a.udpMux = nil
	}
	a.conn = nil
	return errors.Join(errs...)
}

// generateICEString returns length hex chars of cryptographic randomness. The
// alphabet is a subset of the ice-char set of RFC 8839 section 5.3.
func generateICEString(length int) (string, error) {
	b := make([]byte, length/2+1)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:length], nil
}

// iceCandidateSDP renders a candidate as the value of an SDP a=candidate
// attribute, per RFC 8839 section 5.1.
func iceCandidateSDP(c ice.Candidate) string {
	return fmt.Sprintf("candidate:%s %d %s %d %s %d typ %s",
		c.Foundation(),
		c.Component(),
		strings.ToUpper(c.NetworkType().NetworkShort()),
		c.Priority(),
		c.Address(),
		c.Port(),
		c.Type().String(),
	)
}
