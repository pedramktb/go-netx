package netx

import "net"

func NewDemuxDialer(d Dialer, id []byte) Dialer {
	return func() (net.Conn, error) {
		c, err := d()
		if err != nil {
			return nil, err
		}
		return NewDemuxClient(c, id)()
	}
}
