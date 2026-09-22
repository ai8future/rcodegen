package server

import (
	"context"
	"crypto/subtle"
	"net"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// authMetadataKey is the gRPC metadata key carrying the bearer token. gRPC
// lowercases incoming keys, so this is the canonical form callers will match.
const authMetadataKey = "authorization"

// authScheme prefixes the token in the metadata value, matching the
// Authorization header the HTTP API already requires. The two protocols take
// the same credential in the same shape so an operator has one token and one
// spelling to get right.
const authScheme = "Bearer "

// openMethodPrefixes are the services that stay reachable without a token, for
// the same reason HTTP /health does: they answer "is this server alive and what
// does it speak", never "run this". Reflection is what the handoff docs tell
// operators to point grpcurl at, and grpc.health.v1 is the gRPC spelling of the
// /health endpoint that has always been left open for monitoring.
var openMethodPrefixes = []string{
	"/grpc.reflection.v1.ServerReflection/",
	"/grpc.reflection.v1alpha.ServerReflection/",
	"/grpc.health.v1.Health/",
}

// TokenAuth authenticates non-loopback gRPC peers against a shared bearer
// token.
//
// Loopback peers are exempt. The token exists to stop a LAN host from running
// models and cancelling other people's runs on a server bound to 0.0.0.0;
// anything connecting over loopback is already on the machine, where it can run
// the CLIs directly. Exempting it keeps every local tool and script working
// unchanged on a server that has just had a token added.
//
// This guards authorization only. Both listeners are still plaintext, so a
// token on the wire is readable by anything that can see the traffic — it is a
// bound on who may drive the server, not a substitute for TLS.
type TokenAuth struct {
	// expected is the full metadata value a remote peer must present.
	expected []byte
}

// NewTokenAuth builds interceptors requiring the given token from non-loopback
// peers. The caller only installs them when a token is configured; an empty
// token here would demand the literal value "Bearer " from every remote peer.
func NewTokenAuth(token string) *TokenAuth {
	return &TokenAuth{expected: []byte(authScheme + token)}
}

// Unary returns the unary interceptor enforcing the token.
func (a *TokenAuth) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := a.authorize(ctx, info.FullMethod); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// Stream returns the stream interceptor enforcing the token. RunTask and
// RunBundle are streaming RPCs, so without this the unary interceptor would
// guard only status and cancellation while leaving execution open.
func (a *TokenAuth) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := a.authorize(ss.Context(), info.FullMethod); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

// authorize reports whether this call may proceed.
func (a *TokenAuth) authorize(ctx context.Context, fullMethod string) error {
	if isOpenMethod(fullMethod) {
		return nil
	}
	if peerIsLoopback(ctx) {
		return nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return errMissingToken()
	}
	values := md.Get(authMetadataKey)
	if len(values) == 0 {
		return errMissingToken()
	}
	if subtle.ConstantTimeCompare([]byte(values[0]), a.expected) != 1 {
		return errMissingToken()
	}
	return nil
}

// errMissingToken names the metadata key the caller has to set. A wrong token
// and a missing one get the same answer, so the error cannot be used to learn
// which part was wrong.
func errMissingToken() error {
	return status.Errorf(codes.Unauthenticated,
		"missing or invalid credentials: non-loopback peers must send metadata %q as %q<token>",
		authMetadataKey, authScheme)
}

func isOpenMethod(fullMethod string) bool {
	for _, prefix := range openMethodPrefixes {
		if strings.HasPrefix(fullMethod, prefix) {
			return true
		}
	}
	return false
}

// peerIsLoopback reports whether this call arrived from the local machine. A
// call with no peer information fails closed: an unidentifiable caller is
// treated as remote and must present the token.
func peerIsLoopback(ctx context.Context) bool {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return false
	}
	return addrIsLoopback(p.Addr)
}

func addrIsLoopback(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	switch addr.Network() {
	case "unix", "unixgram", "unixpacket":
		// A unix socket has no route off the machine.
		return true
	}
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.IP != nil && tcp.IP.IsLoopback()
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	// Strip any IPv6 zone ("fe80::1%en0"), which ParseIP rejects.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
