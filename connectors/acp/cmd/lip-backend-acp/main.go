package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"sync"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/acp/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"google.golang.org/grpc"
)

func main() {
	listen := flag.String("listen", "", "loopback listen address (optional; production uses LIP_PLUGIN_CHANNEL_PIPE)")
	flag.Parse()

	svc := service.New()
	desc, err := svc.Describe(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "describe: %v\n", err)
		os.Exit(1)
	}
	offer := backendplugin.ProtocolOffer{
		Major: desc.ProtocolMajor, Minor: desc.ProtocolMinor,
		DisableTransportRetries: true, Features: desc.Features,
	}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(offer, svc))

	if pipe := os.Getenv("LIP_PLUGIN_CHANNEL_PIPE"); pipe != "" {
		c, err := dialNamedPipe(pipe)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pipe dial: %v\n", err)
			os.Exit(1)
		}
		_ = gs.Serve(&singleConnListener{conn: c, closed: make(chan struct{})})
		return
	}

	addr := *listen
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "lip-backend-acp listening on %s\n", lis.Addr().String())
	if err := gs.Serve(lis); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

type singleConnListener struct {
	mu     sync.Mutex
	conn   net.Conn
	given  bool
	closed chan struct{}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.given {
		l.given = true
		c := l.conn
		l.mu.Unlock()
		return c, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return l.conn.Close()
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
