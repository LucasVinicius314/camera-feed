package main

import (
	"io"
	"log"
	"net"
	"runtime"

	rtmp "github.com/yutopp/go-rtmp"
)

func init() {
	runtime.LockOSThread()
}

const (
	rtmpAddr  = ":1935"
	streamURL = "rtmp://localhost:1935/live/sauron"
)

func main() {
	relay := newRelayService()
	runWindow(func() {
		startServer(relay)
	})
}

func startServer(relay *relayService) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", rtmpAddr)
	if err != nil {
		log.Fatalf("failed to resolve addr: %v", err)
	}
	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", rtmpAddr, err)
	}
	log.Printf("RTMP server listening on %s", rtmpAddr)

	srv := rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			return conn, &rtmp.ConnConfig{
				Handler: &handler{relay: relay},
				ControlState: rtmp.StreamControlStateConfig{
					DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
				},
			}
		},
	})
	if err := srv.Serve(listener); err != nil {
		log.Fatalf("RTMP server error: %v", err)
	}
}
