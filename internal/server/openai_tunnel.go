package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tunnelclient "github.com/openai/tunnel-client"

	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/logging"
)

// This context key is never populated from HTTP headers, MCP arguments, or
// client metadata. Only the server side of the private in-memory transport can
// authenticate as this principal. All users of a tunnel share this identity;
// access to the tunnel itself is controlled by OpenAI.
type openAITunnelPrincipalKey struct{}

func openAITunnelContext(ctx context.Context, id string) context.Context {
	sum := sha256.Sum256([]byte(id))
	digest := hex.EncodeToString(sum[:])
	principal := auth.Principal{
		ID: "openai_tunnel:" + digest[:24], Kind: "openai_tunnel", SubjectHash: digest,
	}
	return context.WithValue(ctx, openAITunnelPrincipalKey{}, principal)
}

type openAITunnelRuntime struct {
	client  *tunnelclient.Client
	session *mcp.ServerSession
	cancel  context.CancelFunc
	done    chan struct{}
	key     string
	once    sync.Once
	err     error
}

func (r *Runtime) startOpenAITunnel(server *mcp.Server) error {
	cfg, err := config.ResolveOpenAITunnel(r.cfg.OpenAITunnel)
	if err != nil {
		return err
	}
	if !cfg.IsEnabled() {
		return nil
	}
	tunnel, err := connectOpenAITunnel(server, tunnelclient.Config{
		TunnelID: cfg.ID,
		APIKey:   cfg.Key,
	})
	if err != nil {
		return fmt.Errorf("start OpenAI Tunnel: %w", err)
	}
	r.openAITunnel = tunnel
	return nil
}

// connectOpenAITunnel also accepts an SDK base URL for local protocol tests.
// Production deliberately exposes no endpoint override: credentials only go to
// the official default HTTPS control plane, not a workspace-supplied URL.
func connectOpenAITunnel(server *mcp.Server, cfg tunnelclient.Config) (*openAITunnelRuntime, error) {
	cfg.LogWriter = &tunnelRedactingWriter{writer: os.Stderr, key: cfg.APIKey}
	serverTransport, tunnelTransport := mcp.NewInMemoryTransports()
	client, err := tunnelclient.New(cfg, tunnelTransport)
	if err != nil {
		return nil, redactTunnelError(err, cfg.APIKey)
	}
	ctx, cancel := context.WithCancel(openAITunnelContext(context.Background(), cfg.TunnelID))
	session, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		return nil, redactTunnelError(err, cfg.APIKey)
	}
	tunnel := &openAITunnelRuntime{
		client: client, session: session, cancel: cancel, key: cfg.APIKey,
		done: make(chan struct{}),
	}
	startCtx, cancelStart := context.WithTimeout(context.Background(), 15*time.Second)
	err = client.Start(startCtx)
	cancelStart()
	if err != nil {
		cancel()
		_ = session.Close()
		return nil, redactTunnelError(err, cfg.APIKey)
	}
	log := logging.With("component", "openai_tunnel")
	log.Info("started; awaiting first successful control-plane poll", "tunnel_id", cfg.TunnelID)
	go func() {
		defer close(tunnel.done)
		select {
		case <-ctx.Done():
		case <-client.Ready():
			// Ready is a one-time successful round trip, not continuous health.
			log.Info("first control-plane poll succeeded", "tunnel_id", cfg.TunnelID)
		}
	}()
	return tunnel, nil
}

func (t *openAITunnelRuntime) Close() error {
	if t == nil {
		return nil
	}
	t.once.Do(func() {
		// Stop fetching/forwarding before tearing down MCP or durable state.
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		t.err = redactTunnelError(t.client.Stop(ctx), t.key)
		cancel()
		t.cancel()
		if err := t.session.Close(); t.err == nil {
			t.err = err
		}
		<-t.done
		logging.With("component", "openai_tunnel").Info("stopped")
	})
	return t.err
}

func redactTunnelError(err error, key string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactTunnelText(err.Error(), key))
}

func redactTunnelText(text, key string) string {
	if key == "" {
		return text
	}
	return strings.ReplaceAll(text, key, "[REDACTED]")
}

// SDK slog records are emitted with one Write per record. Keep its diagnostics
// while removing credentials from both SDK output and returned startup errors.
type tunnelRedactingWriter struct {
	writer io.Writer
	key    string
	mu     sync.Mutex
}

func (w *tunnelRedactingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := io.WriteString(w.writer, redactTunnelText(string(p), w.key))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
