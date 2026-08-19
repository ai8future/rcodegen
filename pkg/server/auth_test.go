package server

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"rcodegen/pkg/server/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
	reflectpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"
)

const testToken = "s3cret-token"

// testMethod is a guarded RPC: anything that is not on the open list.
const testMethod = "/rserve.RServe/GetStatus"

// fakeAddr is a peer address that is neither TCP nor unix — the shape an
// in-memory or otherwise unusual transport reports.
type fakeAddr struct{ network, addr string }

func (a fakeAddr) Network() string { return a.network }
func (a fakeAddr) String() string  { return a.addr }

// fakeServerStream carries a context into the stream interceptor. Only
// Context() is called, so the embedded nil interface is never touched.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s fakeServerStream) Context() context.Context { return s.ctx }

func authCtx(addr net.Addr, md metadata.MD) context.Context {
	ctx := context.Background()
	if addr != nil {
		ctx = peer.NewContext(ctx, &peer.Peer{Addr: addr})
	}
	if md != nil {
		ctx = metadata.NewIncomingContext(ctx, md)
	}
	return ctx
}

func bearer(v string) metadata.MD { return metadata.Pairs(authMetadataKey, v) }

func tcpAddr(t *testing.T, hostport string) net.Addr {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", hostport)
	if err != nil {
		t.Fatalf("resolve %s: %v", hostport, err)
	}
	return a
}

// peerCases is the authorization matrix, run against both interceptors so the
// streaming RPCs that actually execute models cannot drift from the unary ones.
func peerCases(t *testing.T) []struct {
	name    string
	addr    net.Addr
	md      metadata.MD
	wantErr bool
} {
	t.Helper()
	return []struct {
		name    string
		addr    net.Addr
		md      metadata.MD
		wantErr bool
	}{
		// Loopback is exempt: these callers are already on the machine and could
		// run the CLIs directly.
		{name: "loopback v4 without token", addr: tcpAddr(t, "127.0.0.1:51000")},
		{name: "loopback v4 alias without token", addr: tcpAddr(t, "127.0.0.53:51000")},
		{name: "loopback v6 without token", addr: tcpAddr(t, "[::1]:51000")},
		{name: "unix socket without token", addr: &net.UnixAddr{Name: "/tmp/rserve.sock", Net: "unix"}},
		// The exemption wins over a bad credential rather than being overridden
		// by it, so a local tool with a stale token keeps working.
		{name: "loopback with wrong token", addr: tcpAddr(t, "127.0.0.1:51000"), md: bearer("Bearer wrong")},

		// Everything else must present the token.
		{name: "remote without metadata", addr: tcpAddr(t, "203.0.113.7:51000"), wantErr: true},
		{name: "remote with unrelated metadata only", addr: tcpAddr(t, "203.0.113.7:51000"), md: metadata.Pairs("x-request-id", "abc"), wantErr: true},
		{name: "remote with empty authorization", addr: tcpAddr(t, "203.0.113.7:51000"), md: bearer(""), wantErr: true},
		{name: "remote with wrong token", addr: tcpAddr(t, "203.0.113.7:51000"), md: bearer("Bearer wrong"), wantErr: true},
		{name: "remote with token missing scheme", addr: tcpAddr(t, "203.0.113.7:51000"), md: bearer(testToken), wantErr: true},
		{name: "remote with truncated token", addr: tcpAddr(t, "203.0.113.7:51000"), md: bearer("Bearer " + testToken[:len(testToken)-1]), wantErr: true},
		{name: "remote v6 without token", addr: tcpAddr(t, "[2001:db8::1]:51000"), wantErr: true},
		{name: "remote with correct token", addr: tcpAddr(t, "203.0.113.7:51000"), md: bearer("Bearer " + testToken)},

		// An unidentifiable peer fails closed: no peer info means no evidence the
		// caller is local, so it is treated as remote.
		{name: "no peer information without token", wantErr: true},
		{name: "no peer information with correct token", md: bearer("Bearer " + testToken)},
		{name: "non-IP peer address without token", addr: fakeAddr{network: "bufconn", addr: "bufconn"}, wantErr: true},
		{name: "non-IP peer address with correct token", addr: fakeAddr{network: "bufconn", addr: "bufconn"}, md: bearer("Bearer " + testToken)},
	}
}

func TestTokenAuth_Unary(t *testing.T) {
	auth := NewTokenAuth(testToken)
	interceptor := auth.Unary()

	for _, tc := range peerCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := func(ctx context.Context, req any) (any, error) {
				called = true
				return "ok", nil
			}
			resp, err := interceptor(authCtx(tc.addr, tc.md), nil, &grpc.UnaryServerInfo{FullMethod: testMethod}, handler)

			if tc.wantErr {
				if status.Code(err) != codes.Unauthenticated {
					t.Errorf("code = %v (err %v), want Unauthenticated", status.Code(err), err)
				}
				if called {
					t.Error("handler ran for an unauthenticated call")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !called || resp != "ok" {
				t.Errorf("handler called = %v, resp = %v; want true, ok", called, resp)
			}
		})
	}
}

func TestTokenAuth_Stream(t *testing.T) {
	auth := NewTokenAuth(testToken)
	interceptor := auth.Stream()

	for _, tc := range peerCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := func(srv any, ss grpc.ServerStream) error {
				called = true
				return nil
			}
			stream := fakeServerStream{ctx: authCtx(tc.addr, tc.md)}
			err := interceptor(nil, stream, &grpc.StreamServerInfo{FullMethod: "/rserve.RServe/RunTask"}, handler)

			if tc.wantErr {
				if status.Code(err) != codes.Unauthenticated {
					t.Errorf("code = %v (err %v), want Unauthenticated", status.Code(err), err)
				}
				if called {
					t.Error("handler ran for an unauthenticated stream")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !called {
				t.Error("handler did not run for an authorized stream")
			}
		})
	}
}

// The rejection has to name the metadata key, because a caller who gets back
// only "unauthenticated" has no way to learn what to send.
func TestTokenAuth_ErrorNamesMetadataKeyWithoutLeakingToken(t *testing.T) {
	auth := NewTokenAuth(testToken)
	_, err := auth.Unary()(authCtx(tcpAddr(t, "203.0.113.7:51000"), nil), nil,
		&grpc.UnaryServerInfo{FullMethod: testMethod},
		func(context.Context, any) (any, error) { return nil, nil })

	msg := status.Convert(err).Message()
	if !strings.Contains(msg, authMetadataKey) {
		t.Errorf("message %q does not name the %q metadata key", msg, authMetadataKey)
	}
	if !strings.Contains(msg, strings.TrimSpace(authScheme)) {
		t.Errorf("message %q does not name the %q scheme", msg, authScheme)
	}
	if strings.Contains(msg, testToken) {
		t.Errorf("message %q leaks the configured token", msg)
	}
}

func TestTokenAuth_OpenMethodsSkipTokenForRemotePeers(t *testing.T) {
	auth := NewTokenAuth(testToken)
	remote := authCtx(tcpAddr(t, "203.0.113.7:51000"), nil)

	open := []string{
		"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
		"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo",
		"/grpc.health.v1.Health/Check",
		"/grpc.health.v1.Health/Watch",
	}
	for _, method := range open {
		if err := auth.authorize(remote, method); err != nil {
			t.Errorf("%s should be open without a token, got %v", method, err)
		}
	}

	// Every RPC that runs work or reads run state stays guarded. A prefix match
	// must not accidentally open a service that merely starts with a shared
	// string.
	guarded := []string{
		"/rserve.RServe/RunTask",
		"/rserve.RServe/RunBundle",
		"/rserve.RServe/CancelRun",
		"/rserve.RServe/GetStatus",
		"/rserve.RServe/ListTasks",
		"/grpc.reflection.v1.ServerReflectionEvil/Steal",
		"/grpc.health.v1.HealthAdmin/Drain",
	}
	for _, method := range guarded {
		if err := auth.authorize(remote, method); status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s should require a token, got %v", method, err)
		}
	}
}

// Timing cannot be asserted reliably in a unit test, so this pins the property
// at the source level: the comparison must stay constant-time.
func TestTokenAuth_UsesConstantTimeCompare(t *testing.T) {
	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatalf("read auth.go: %v", err)
	}
	if !strings.Contains(string(src), "subtle.ConstantTimeCompare") {
		t.Error("auth.go no longer uses subtle.ConstantTimeCompare for the token comparison")
	}
	for _, naive := range []string{"values[0] == ", "== string(a.expected)", "string(a.expected) =="} {
		if strings.Contains(string(src), naive) {
			t.Errorf("auth.go contains a non-constant-time token comparison: %q", naive)
		}
	}
}

// spoofedPeerListener reports a fixed remote address for every accepted
// connection, so a test can drive a real gRPC server as if the client were on
// the LAN. gRPC takes the peer address straight from conn.RemoteAddr().
type spoofedPeerListener struct {
	net.Listener
	remote net.Addr
}

func (l spoofedPeerListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return spoofedPeerConn{Conn: conn, remote: l.remote}, nil
}

type spoofedPeerConn struct {
	net.Conn
	remote net.Addr
}

func (c spoofedPeerConn) RemoteAddr() net.Addr { return c.remote }

// startAuthTestServer runs a real gRPC server with the auth interceptors
// installed the way main.go installs them, and returns a client connection. A
// non-nil remote makes every connection look like it came from off-machine.
func startAuthTestServer(t *testing.T, remote net.Addr) *grpc.ClientConn {
	t.Helper()

	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var lis net.Listener = base
	if remote != nil {
		lis = spoofedPeerListener{Listener: base, remote: remote}
	}

	auth := NewTokenAuth(testToken)
	gs := grpc.NewServer(
		grpc.ChainUnaryInterceptor(auth.Unary()),
		grpc.ChainStreamInterceptor(auth.Stream()),
	)
	pb.RegisterRServeServer(gs, NewServer(nil, map[string]ToolFactory{}, NewRunRegistry(1), nil))
	reflection.Register(gs)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient(base.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestTokenAuth_EndToEndRemotePeer(t *testing.T) {
	conn := startAuthTestServer(t, tcpAddr(t, "203.0.113.7:40001"))
	client := pb.NewRServeClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("unary without token is refused", func(t *testing.T) {
		if _, err := client.GetStatus(ctx, &pb.GetStatusRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Errorf("code = %v (err %v), want Unauthenticated", status.Code(err), err)
		}
	})

	t.Run("unary with token succeeds", func(t *testing.T) {
		authed := metadata.AppendToOutgoingContext(ctx, authMetadataKey, "Bearer "+testToken)
		resp, err := client.GetStatus(authed, &pb.GetStatusRequest{})
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if resp.MaxConcurrent != 1 {
			t.Errorf("MaxConcurrent = %d, want 1", resp.MaxConcurrent)
		}
	})

	t.Run("streaming run without token is refused", func(t *testing.T) {
		stream, err := client.RunTask(ctx, &pb.RunTaskRequest{Tool: "claude", Task: "audit"})
		if err == nil {
			_, err = stream.Recv()
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("code = %v (err %v), want Unauthenticated", status.Code(err), err)
		}
	})

	t.Run("cancel without token is refused", func(t *testing.T) {
		if _, err := client.CancelRun(ctx, &pb.CancelRunRequest{RunId: "abc"}); status.Code(err) != codes.Unauthenticated {
			t.Errorf("code = %v (err %v), want Unauthenticated", status.Code(err), err)
		}
	})

	// Reflection is deliberately left open, the same way HTTP /health is: it is
	// how the handoff docs tell operators to discover the schema with grpcurl.
	t.Run("reflection is reachable without a token", func(t *testing.T) {
		stream, err := reflectpb.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
		if err != nil {
			t.Fatalf("open reflection stream: %v", err)
		}
		if err := stream.Send(&reflectpb.ServerReflectionRequest{
			MessageRequest: &reflectpb.ServerReflectionRequest_ListServices{ListServices: "*"},
		}); err != nil {
			t.Fatalf("send reflection request: %v", err)
		}
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv reflection response: %v", err)
		}
		listing := resp.GetListServicesResponse()
		if listing == nil {
			t.Fatalf("reflection response held no service listing: %v", resp.GetMessageResponse())
		}
		var found bool
		for _, svc := range listing.Service {
			if svc.Name == "rserve.RServe" {
				found = true
			}
		}
		if !found {
			t.Errorf("reflection listing missing rserve.RServe: %v", listing.Service)
		}
	})
}

// The whole point of the loopback exemption: adding a token must not break the
// local CLI and tooling workflows that never sent one.
func TestTokenAuth_EndToEndLoopbackPeerNeedsNoToken(t *testing.T) {
	conn := startAuthTestServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := pb.NewRServeClient(conn).GetStatus(ctx, &pb.GetStatusRequest{})
	if err != nil {
		t.Fatalf("loopback GetStatus without token: %v", err)
	}
	if resp.MaxConcurrent != 1 {
		t.Errorf("MaxConcurrent = %d, want 1", resp.MaxConcurrent)
	}
}
