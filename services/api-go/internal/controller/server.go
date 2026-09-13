// runs the HTTP server and handles graceful shutdown.
package controller

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type ServerOptions struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	Logger            *slog.Logger
}

// serve runs until cancellation, then drains in-flight requests.
func Serve(ctx context.Context, opts ServerOptions, handler http.Handler) error {
	// let in-flight requests finish after the shutdown signal cancels ctx.
	server := &http.Server{
		Addr:              opts.Addr,
		Handler:           handler,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	// open the listener first so bind failures return synchronously.
	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}

	serveErr := make(chan error, 1)
	go func() {
		opts.Logger.Info("api listening", "addr", listener.Addr().String())
		serveErr <- server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opts.ShutdownTimeout)
	defer cancel()

	opts.Logger.Info("api shutting down", "timeout", opts.ShutdownTimeout.String())
	if err := server.Shutdown(shutdownCtx); err != nil {
		// Force-close requests that outlive the shutdown deadline.
		_ = server.Close()
		return err
	}

	return nil
}
