// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

type DigestAuth struct {
	Username string
	Password string
	Realm    string
	Expire   time.Duration
}

func (a *DigestAuth) expire() time.Duration {
	if a.Expire > 0 {
		return a.Expire
	}
	return 5 * time.Second
}

type DigestAuthServer struct {
	mu    sync.Mutex
	cache map[string]*digest.Challenge
}

func NewDigestServer() *DigestAuthServer {
	t := &DigestAuthServer{
		cache: make(map[string]*digest.Challenge),
	}
	return t
}

func (s *DigestAuthServer) Authorize(d *DialogServerSession, auth DigestAuth) error {
	if auth.Realm == "" {
		auth.Realm = "sipgo"
	}

	// https://www.rfc-editor.org/rfc/rfc2617#page-6
	req := d.InviteRequest
	res, err := s.AuthorizeRequest(req, auth)

	return errors.Join(err, d.WriteResponse(res))
	// h := req.GetHeader("Authorization")

	// if h == nil {
	// 	nonce, err := generateNonce()
	// 	if err != nil {
	// 		return err
	// 	}

	// 	chal := &digest.Challenge{
	// 		Realm: auth.Realm,
	// 		Nonce: nonce,
	// 		// Opaque:    "sipgo",
	// 		Algorithm: "MD5",
	// 	}

	// 	res := sip.NewResponseFromRequest(req, 401, "Unathorized", nil)
	// 	res.AppendHeader(sip.NewHeader("WWW-Authenticate", chal.String()))
	// 	if err := d.WriteResponse(res); err != nil {
	// 		return err
	// 	}

	// 	s.mu.Lock()
	// 	s.cache[nonce] = chal
	// 	s.mu.Unlock()
	// 	time.AfterFunc(auth.expire(), func() {
	// 		s.mu.Lock()
	// 		delete(s.cache, nonce)
	// 		s.mu.Unlock()
	// 	})

	// 	return fmt.Errorf("challenged")
	// }

	// cred, err := digest.ParseCredentials(h.Value())
	// if err != nil {
	// 	d.Respond(sip.StatusBadRequest, "Bad credentials", nil)
	// 	return fmt.Errorf("Parsing creds failed: %w", err)
	// }

	// chal, exists := s.cache[cred.Nonce]
	// if !exists {
	// 	return fmt.Errorf("challenge not found")
	// }

	// // Make digest and compare response
	// digCred, err := digest.Digest(chal, digest.Options{
	// 	Method:   "INVITE",
	// 	URI:      cred.URI,
	// 	Username: auth.Username,
	// 	Password: auth.Password,
	// })

	// if err != nil {
	// 	log.Error().Err(err).Msg("Calc digest failed")
	// 	d.Respond(sip.StatusUnauthorized, "Bad credentials", nil)
	// 	return fmt.Errorf("digest calculation failed: %w", err)
	// }

	// if cred.Response != digCred.Response {
	// 	d.Respond(401, "Unathorized", nil)
	// 	return fmt.Errorf("non matching creds, unathorized")
	// }
	// return nil
}

var (
	ErrDigestAuthNoChallenge = errors.New("no challenge")
	ErrDigestAuthBadCreds    = errors.New("bad credentials")
)

// AuthorizeRequest authorizes request. Returns SIP response that can be passed with error
func (s *DigestAuthServer) AuthorizeRequest(req *sip.Request, auth DigestAuth) (res *sip.Response, err error) {
	h := req.GetHeader("Authorization")
	// https://www.rfc-editor.org/rfc/rfc2617#page-6

	if h == nil {
		nonce, err := generateNonce()
		if err != nil {
			return sip.NewResponseFromRequest(req, sip.StatusInternalServerError, "Internal Server Error", nil), err
		}

		chal := &digest.Challenge{
			Realm: auth.Realm,
			Nonce: nonce,
			// Opaque:    "sipgo",
			Algorithm: "MD5",
		}

		res := sip.NewResponseFromRequest(req, 401, "Unathorized", nil)
		res.AppendHeader(sip.NewHeader("WWW-Authenticate", chal.String()))

		s.mu.Lock()
		s.cache[nonce] = chal
		s.mu.Unlock()
		time.AfterFunc(auth.expire(), func() {
			s.mu.Lock()
			delete(s.cache, nonce)
			s.mu.Unlock()
		})

		return res, nil
	}

	cred, err := digest.ParseCredentials(h.Value())
	if err != nil {
		return sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil), err
	}

	chal, exists := s.cache[cred.Nonce]
	if !exists {
		return sip.NewResponseFromRequest(req, sip.StatusUnauthorized, "Unauthorized", nil), ErrDigestAuthNoChallenge
	}

	// Make digest and compare response
	digCred, err := digest.Digest(chal, digest.Options{
		Method:   req.Method.String(),
		URI:      cred.URI,
		Username: auth.Username,
		Password: auth.Password,
	})

	if err != nil {
		// Mostly due to unsupported digest alg
		return sip.NewResponseFromRequest(req, sip.StatusForbidden, "Forbidden", nil), err
	}

	if cred.Response != digCred.Response {
		return sip.NewResponseFromRequest(req, sip.StatusUnauthorized, "Unauthorized", nil), ErrDigestAuthBadCreds
	}

	return sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil), nil
}

func generateNonce() (string, error) {
	nonceBytes := make([]byte, 32)
	_, err := rand.Read(nonceBytes)
	if err != nil {
		return "", fmt.Errorf("could not generate nonce")
	}

	return base64.URLEncoding.EncodeToString(nonceBytes), nil
}
