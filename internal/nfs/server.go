package nfs

import (
	"context"
	"net"

	"github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"

	"invariant/internal/files"
)

// Server encapsulates an NFS server.
type Server struct {
	listener net.Listener
	server   *nfs.Server
}

// Start starts an NFS server listening on the provided address, serving the given files.Files service.
func Start(ctx context.Context, listenAddr string, fsrv files.Files, rootNodeID uint64) (*Server, error) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	bfs := NewFS(fsrv, rootNodeID)
	handler := nfshelper.NewNullAuthHandler(bfs)
	cacheHelper := nfshelper.NewCachingHandler(handler, 1024)

	done := make(chan error, 1)

	srv := &Server{
		listener: listener,
		server:   &nfs.Server{Handler: cacheHelper},
	}

	go func() {
		err := srv.server.Serve(listener)
		// Wait for closing
		done <- err
	}()

	return srv, nil
}

// Close stops the NFS server.
func (s *Server) Close() error {
	return s.listener.Close()
}
