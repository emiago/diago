// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package media

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/pion/ice/v4"
)

// ICEIPFamily selects the IP families used to gather UDP ICE candidates.
type ICEIPFamily uint8

const (
	ICEIPFamilyIPv4 ICEIPFamily = iota + 1
	ICEIPFamilyIPv6
)

// ICECandidateType selects which kinds of UDP ICE candidates are gathered.
type ICECandidateType uint8

const (
	ICECandidateHost ICECandidateType = iota + 1
	ICECandidateServerReflexive
	ICECandidateRelay
)

// ICETimeouts controls ICE connection establishment and liveness detection.
// Zero values use the defaults below.
type ICETimeouts struct {
	Disconnected time.Duration
	Failed       time.Duration
	Keepalive    time.Duration
}

const (
	DefaultICEDisconnectedTimeout = 5 * time.Second
	DefaultICEFailedTimeout       = 25 * time.Second
	DefaultICEKeepaliveInterval   = 2 * time.Second
)

// ICEPhase identifies the part of ICE that produced an ICEError.
type ICEPhase uint8

const (
	ICEPhaseGathering ICEPhase = iota + 1
	ICEPhaseConnection
)

func (p ICEPhase) String() string {
	switch p {
	case ICEPhaseGathering:
		return "gathering"
	case ICEPhaseConnection:
		return "connection"
	default:
		return "unknown"
	}
}

var (
	ErrICEGathering  = errors.New("ICE candidate gathering failed")
	ErrICEConnection = errors.New("ICE connection failed")
)

// ICEError reports an ICE failure with enough information for callers to
// distinguish configuration/gathering, initial connection and later liveness
// failures. errors.Is works with ErrICEGathering and ErrICEConnection;
// errors.As exposes the phase and underlying error.
type ICEError struct {
	Phase ICEPhase
	Err   error
}

func (e *ICEError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("ICE %s failed", e.Phase)
	}
	return fmt.Sprintf("ICE %s failed: %v", e.Phase, e.Err)
}

func (e *ICEError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ICEError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch e.Phase {
	case ICEPhaseGathering:
		return target == ErrICEGathering
	case ICEPhaseConnection:
		return target == ErrICEConnection
	default:
		return false
	}
}

func webRTCNetworkTypes(conf MediaSessionWebrtcConfig) ([]ice.NetworkType, error) {
	if len(conf.IPFamilies) > 0 && len(conf.NetworkTypes) > 0 {
		return nil, fmt.Errorf("ICE IPFamilies and deprecated NetworkTypes cannot both be set")
	}
	if len(conf.NetworkTypes) > 0 {
		result := make([]ice.NetworkType, 0, len(conf.NetworkTypes))
		for _, networkType := range conf.NetworkTypes {
			if networkType != ice.NetworkTypeUDP4 && networkType != ice.NetworkTypeUDP6 {
				return nil, fmt.Errorf("direct WebRTC supports UDP ICE only, got network type %s", networkType)
			}
			if !containsICENetworkType(result, networkType) {
				result = append(result, networkType)
			}
		}
		return result, nil
	}
	if len(conf.IPFamilies) == 0 {
		return []ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}, nil
	}
	result := make([]ice.NetworkType, 0, len(conf.IPFamilies))
	for _, family := range conf.IPFamilies {
		var networkType ice.NetworkType
		switch family {
		case ICEIPFamilyIPv4:
			networkType = ice.NetworkTypeUDP4
		case ICEIPFamilyIPv6:
			networkType = ice.NetworkTypeUDP6
		default:
			return nil, fmt.Errorf("unsupported ICE IP family %d", family)
		}
		if !containsICENetworkType(result, networkType) {
			result = append(result, networkType)
		}
	}
	return result, nil
}

func containsICENetworkType(types []ice.NetworkType, candidate ice.NetworkType) bool {
	for _, networkType := range types {
		if networkType == candidate {
			return true
		}
	}
	return false
}

func webRTCCandidateTypes(conf MediaSessionWebrtcConfig) ([]ice.CandidateType, error) {
	if len(conf.CandidateTypes) == 0 {
		return []ice.CandidateType{
			ice.CandidateTypeHost,
			ice.CandidateTypeServerReflexive,
			ice.CandidateTypeRelay,
		}, nil
	}
	result := make([]ice.CandidateType, 0, len(conf.CandidateTypes))
	for _, candidateType := range conf.CandidateTypes {
		var pionType ice.CandidateType
		switch candidateType {
		case ICECandidateHost:
			pionType = ice.CandidateTypeHost
		case ICECandidateServerReflexive:
			pionType = ice.CandidateTypeServerReflexive
		case ICECandidateRelay:
			pionType = ice.CandidateTypeRelay
		default:
			return nil, fmt.Errorf("unsupported ICE candidate type %d", candidateType)
		}
		if !containsICECandidateType(result, pionType) {
			result = append(result, pionType)
		}
	}
	return result, nil
}

func containsICECandidateType(types []ice.CandidateType, candidate ice.CandidateType) bool {
	for _, candidateType := range types {
		if candidateType == candidate {
			return true
		}
	}
	return false
}

func normalizeICETimeouts(timeouts ICETimeouts) (ICETimeouts, error) {
	if timeouts.Disconnected < 0 || timeouts.Failed < 0 || timeouts.Keepalive < 0 {
		return ICETimeouts{}, fmt.Errorf("ICE timeouts cannot be negative")
	}
	if timeouts.Disconnected == 0 {
		timeouts.Disconnected = DefaultICEDisconnectedTimeout
	}
	if timeouts.Failed == 0 {
		timeouts.Failed = DefaultICEFailedTimeout
	}
	if timeouts.Keepalive == 0 {
		timeouts.Keepalive = DefaultICEKeepaliveInterval
	}
	return timeouts, nil
}

func webRTCIPFilter(filter func(netip.Addr) bool) func(net.IP) bool {
	if filter == nil {
		return nil
	}
	return func(ip net.IP) bool {
		addr, ok := netip.AddrFromSlice(ip)
		return ok && filter(addr.Unmap())
	}
}
