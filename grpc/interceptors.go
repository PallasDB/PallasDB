package grpcapi

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoveryUnaryInterceptor converts a panic in a unary handler into
// codes.Internal instead of taking the process down. The storage layer asserts
// with panics on hot paths (db.check), so a single malformed request would
// otherwise kill the server; on the Raft apply path the same request kills
// every replica deterministically.
func RecoveryUnaryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				resp = nil
				err = recoveredError(ctx, logger, info.FullMethod, r)
			}
		}()
		return handler(ctx, req)
	}
}

// RecoveryStreamInterceptor is the streaming counterpart of
// RecoveryUnaryInterceptor.
func RecoveryStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = recoveredError(ss.Context(), logger, info.FullMethod, r)
			}
		}()
		return handler(srv, ss)
	}
}

func recoveredError(ctx context.Context, logger *slog.Logger, method string, recovered any) error {
	if logger == nil {
		logger = slog.Default()
	}
	logger.ErrorContext(ctx, "grpc handler panic",
		"method", method,
		"panic", recovered,
		"stack", string(debug.Stack()),
	)
	// The panic value is deliberately not echoed to the client: it routinely
	// carries internal invariants and key material.
	return status.Errorf(codes.Internal, "internal error handling %s", method)
}

// LoggingUnaryInterceptor logs the method, status code and duration of every
// unary call.
func LoggingUnaryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logger.InfoContext(ctx, "grpc request",
			"method", info.FullMethod,
			"code", status.Code(err).String(),
			"duration", time.Since(start),
		)
		return resp, err
	}
}

// LoggingStreamInterceptor is the streaming counterpart of
// LoggingUnaryInterceptor. Without it, streaming RPCs such as Range and Query
// are completely unlogged.
func LoggingStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		counted := &countingServerStream{ServerStream: ss}
		err := handler(srv, counted)
		logger.InfoContext(ss.Context(), "grpc stream",
			"method", info.FullMethod,
			"code", status.Code(err).String(),
			"messages_sent", counted.sent,
			"duration", time.Since(start),
		)
		return err
	}
}

// countingServerStream counts messages sent so stream logs carry the one number
// a unary log line cannot: how much the stream actually produced.
type countingServerStream struct {
	grpc.ServerStream
	sent int
}

func (s *countingServerStream) SendMsg(m any) error {
	if err := s.ServerStream.SendMsg(m); err != nil {
		return err
	}
	s.sent++
	return nil
}
