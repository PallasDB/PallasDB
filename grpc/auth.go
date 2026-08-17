package grpcapi

import (
	"context"
	"crypto/subtle"
	"slices"
	"strings"

	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// clusterServicePrefix is the method prefix guarded by the auth interceptor.
// Membership changes hand out a full replica and a vote, so they are the one
// surface that must not be reachable by anyone who can reach the port.
var clusterServicePrefix = "/" + pbv1.ClusterService_ServiceDesc.ServiceName + "/"

const authorizationMetadataKey = "authorization"

// AuthConfig authenticates ClusterService callers. A request is accepted when
// it satisfies any configured mechanism; with no mechanism configured the
// interceptor is never installed and the service is open, which is the default
// for a local single-node deployment.
type AuthConfig struct {
	token string
	// peerAuth requires a verified client certificate.
	peerAuth bool
	// allowedNames, when non-empty, restricts the accepted certificate common
	// names and DNS SANs.
	allowedNames []string
}

func (a *AuthConfig) withToken(token string) *AuthConfig {
	out := a.clone()
	out.token = token
	return out
}

func (a *AuthConfig) withPeerNames(names []string) *AuthConfig {
	out := a.clone()
	out.peerAuth = true
	out.allowedNames = append(out.allowedNames, names...)
	return out
}

func (a *AuthConfig) clone() *AuthConfig {
	if a == nil {
		return &AuthConfig{}
	}
	out := *a
	out.allowedNames = slices.Clone(a.allowedNames)
	return &out
}

// Authenticate reports whether ctx carries an accepted identity. When several
// mechanisms are configured, satisfying any one of them is enough.
func (a *AuthConfig) Authenticate(ctx context.Context) error {
	if a == nil {
		return nil
	}
	var failure error
	if a.token != "" {
		switch presented := tokenFromContext(ctx); {
		case presented == "":
			failure = status.Error(codes.Unauthenticated, "missing authorization token")
		case subtle.ConstantTimeCompare([]byte(presented), []byte(a.token)) == 1:
			return nil
		default:
			failure = status.Error(codes.Unauthenticated, "invalid authentication token")
		}
	}
	if a.peerAuth {
		err := a.authenticatePeer(ctx)
		if err == nil {
			return nil
		}
		failure = err
	}
	return failure
}

func (a *AuthConfig) authenticatePeer(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no peer information")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return status.Error(codes.Unauthenticated, "connection is not mutually authenticated")
	}
	// VerifiedChains is populated only after the certificate has been checked
	// against the configured client CA.
	if len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return status.Error(codes.Unauthenticated, "no verified client certificate")
	}
	if len(a.allowedNames) == 0 {
		return nil
	}
	leaf := tlsInfo.State.VerifiedChains[0][0]
	if slices.Contains(a.allowedNames, leaf.Subject.CommonName) {
		return nil
	}
	for _, name := range leaf.DNSNames {
		if slices.Contains(a.allowedNames, name) {
			return nil
		}
	}
	return status.Errorf(codes.PermissionDenied, "client %q is not allowed to manage cluster membership", leaf.Subject.CommonName)
}

func tokenFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, value := range md.Get(authorizationMetadataKey) {
		if after, found := strings.CutPrefix(value, "Bearer "); found {
			return after
		}
	}
	return ""
}

// UnaryInterceptor gates unary ClusterService methods.
func (a *AuthConfig) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if guarded(info.FullMethod) {
			if err := a.Authenticate(ctx); err != nil {
				return nil, err
			}
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor gates streaming ClusterService methods. ClusterService has
// none today; the interceptor exists so adding one cannot silently open a hole.
func (a *AuthConfig) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if guarded(info.FullMethod) {
			if err := a.Authenticate(ss.Context()); err != nil {
				return err
			}
		}
		return handler(srv, ss)
	}
}

func guarded(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, clusterServicePrefix)
}
