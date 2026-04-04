package netx

import (
	"net"
	"sync"
)

type demuxAccept struct {
	conn net.Conn
	err  error
}

type demuxListener struct {
	net.Listener
	demux    func(net.Conn) (net.Listener, error)
	accQueue chan demuxAccept
	once     sync.Once
}

// NewDemuxListener creates a new Demux that listens on an underlying net.Listener.
// This is different from NewDemux in that instead of Demuxing a previously Muxed listener,
// it creates a new Demux for each accepted connection from the underlying listener,
// which allows an addition of an ID to the current connection routing logic based on the remote address of the accepted connection,
// instead of ignoring it and relying solely on the session ID in the packet for routing.
func NewDemuxListener(l net.Listener, idMask uint8, opts ...DemuxOption) net.Listener {
	return &demuxListener{
		Listener: l,
		demux: func(c net.Conn) (net.Listener, error) {
			return NewDemux(c, idMask, opts...)
		},
		accQueue: make(chan demuxAccept, 1),
	}
}

func (dl *demuxListener) Accept() (net.Conn, error) {
	dl.once.Do(func() {
		go func() {
			defer close(dl.accQueue)
			for {
				c, err := dl.Listener.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					l, err := dl.demux(c)
					if err != nil {
						c.Close()
						return
					}
					defer l.Close()
					for {
						conn, err := l.Accept()
						dl.accQueue <- demuxAccept{conn, err}
						if err != nil {
							return
						}
					}
				}(c)
			}
		}()
	})
	r, ok := <-dl.accQueue
	if !ok {
		return nil, net.ErrClosed
	}
	return r.conn, r.err
}
