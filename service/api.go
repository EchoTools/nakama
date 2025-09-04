// Copyright 2018 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/golang-jwt/jwt/v5"
	grpcgw "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip" // enable gzip compression on server for grpc
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

var once sync.Once

// Used as part of JSON input validation.
const byteBracket byte = '{'

// Keys used for storing/retrieving user information in the context of a request after authentication.
type ctxUserIDKey = struct{}
type ctxUsernameKey = struct{}
type ctxVarsKey = struct{}
type ctxExpiryKey = struct{}
type ctxTokenIDKey = struct{}
type ctxTokenIssuedAtKey = struct{}

type ctxFullMethodKey struct{}

func parseBasicAuth(auth string) (username, password string, ok bool) {
	if auth == "" {
		return
	}
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		return
	}
	c, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return
	}
	cs := string(c)
	s := strings.IndexByte(cs, ':')
	if s < 0 {
		return
	}
	return cs[:s], cs[s+1:], true
}

func parseToken(hmacSecretByte []byte, tokenString string) (userID uuid.UUID, username string, vars map[string]string, exp int64, tokenId string, issuedAt int64, ok bool) {
	jwtToken, err := jwt.ParseWithClaims(tokenString, &server.SessionTokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		if s, ok := token.Method.(*jwt.SigningMethodHMAC); !ok || s.Hash != crypto.SHA256 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return hmacSecretByte, nil
	})
	if err != nil {
		return
	}
	claims, ok := jwtToken.Claims.(*server.SessionTokenClaims)
	if !ok || !jwtToken.Valid {
		return
	}
	userID, err = uuid.FromString(claims.UserId)
	if err != nil {
		return
	}
	return userID, claims.Username, claims.Vars, claims.ExpiresAt, claims.TokenId, claims.IssuedAt, true
}

func decompressHandler(logger *zap.Logger, h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Content-Encoding") {
		case "gzip":
			gr, err := gzip.NewReader(r.Body)
			if err != nil {
				logger.Debug("Error processing gzip request body, attempting to read uncompressed", zap.Error(err))
				break
			}
			r.Body = gr
		case "deflate":
			r.Body = flate.NewReader(r.Body)
		default:
			// No request compression.
		}
		h.ServeHTTP(w, r)
	}
}

func extractClientAddressFromContext(logger *zap.Logger, ctx context.Context) (string, string) {
	var clientAddr string
	md, _ := metadata.FromIncomingContext(ctx)
	if ips := md.Get("x-forwarded-for"); len(ips) > 0 {
		// Look for gRPC-Gateway / LB header.
		clientAddr = strings.Split(ips[0], ",")[0]
	} else if peerInfo, ok := peer.FromContext(ctx); ok {
		// If missing, try to look up gRPC peer info.
		clientAddr = peerInfo.Addr.String()
	}

	return extractClientAddress(logger, clientAddr, ctx, "context")
}

func extractClientAddressFromRequest(logger *zap.Logger, r *http.Request) (string, string) {
	var clientAddr string
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		clientAddr = ip
	} else if ips := r.Header.Get("x-forwarded-for"); len(ips) > 0 {
		clientAddr = strings.Split(ips, ",")[0]
	} else {
		clientAddr = r.RemoteAddr
	}

	return extractClientAddress(logger, clientAddr, r, "request")
}

func extractClientAddress(logger *zap.Logger, clientAddr string, source interface{}, sourceType string) (string, string) {
	var clientIP, clientPort string

	if clientAddr != "" {
		// It's possible the request metadata had no client address string.

		clientAddr = strings.TrimSpace(clientAddr)
		if host, port, err := net.SplitHostPort(clientAddr); err == nil {
			clientIP = host
			clientPort = port
		} else {
			var addrErr *net.AddrError
			if errors.As(err, &addrErr) {
				switch addrErr.Err {
				case "missing port in address":
					fallthrough
				case "too many colons in address":
					clientIP = clientAddr
				default:
					// Unknown address error, ignore the address.
				}
			}
		}
		// At this point err may still be a non-nil value that's not a *net.AddrError, ignore the address.
	}

	if clientIP == "" {
		if r, isRequest := source.(*http.Request); isRequest {
			source = map[string]interface{}{"headers": r.Header, "remote_addr": r.RemoteAddr}
		}
		logger.Warn("cannot extract client address", zap.String("address_source_type", sourceType), zap.Any("address_source", source))
	}

	return clientIP, clientPort
}

func traceApiBefore(ctx context.Context, logger *zap.Logger, metrics server.Metrics, fullMethodName string, fn func(clientIP, clientPort string) error) error {
	clientIP, clientPort := extractClientAddressFromContext(logger, ctx)
	start := time.Now()

	// Execute the before hook itself.
	err := fn(clientIP, clientPort)

	metrics.ApiBefore(fullMethodName, time.Since(start), err != nil)

	return err
}

func traceApiAfter(ctx context.Context, logger *zap.Logger, metrics server.Metrics, fullMethodName string, fn func(clientIP, clientPort string) error) {
	clientIP, clientPort := extractClientAddressFromContext(logger, ctx)
	start := time.Now()

	// Execute the after hook itself.
	err := fn(clientIP, clientPort)

	metrics.ApiAfter(fullMethodName, time.Since(start), err != nil)
}

func handleRoutingError(ctx context.Context, mux *grpcgw.ServeMux, marshaler grpcgw.Marshaler, w http.ResponseWriter, r *http.Request, httpStatus int) {
	sterr := status.Error(codes.Internal, "Unexpected routing error")
	switch httpStatus {
	case http.StatusBadRequest:
		sterr = status.Error(codes.InvalidArgument, http.StatusText(httpStatus))
	case http.StatusMethodNotAllowed:
		sterr = status.Error(codes.Unimplemented, http.StatusText(httpStatus))
	case http.StatusNotFound:
		sterr = status.Error(codes.NotFound, http.StatusText(httpStatus))
	}

	// Set empty ServerMetadata to prevent logging error on nil metadata.
	grpcgw.DefaultHTTPErrorHandler(grpcgw.NewServerMetadataContext(ctx, grpcgw.ServerMetadata{}), mux, marshaler, w, r, sterr)
}
