package netx

import (
	"net"
	"time"
)

// TaggedConn is a net.Conn extension that in its taggable type allows passing contextual information along with read/write operations.
// This is useful for protocols where the write path requires context from the read path (e.g. associating a response with a specific request in a tunnel),
// or when metadata needs to traverse through layers (e.g. Muxed Transport <-> Encryption <-> Demux).
type TaggedConn interface {
	// ReadTagged reads data into the provided buffer and returns the number of bytes read along with any error encountered.
	// The tag parameter is a pointer to an empty interface that will be populated with contextual information associated with the read data.
	ReadTagged([]byte, *any) (int, error)

	// WriteTagged writes data from the provided buffer and returns the number of bytes written along with any error encountered.
	// The tag parameter provides contextual information that may be required for the write operation, such as associating it with a specific request or metadata.
	// If unsure, you should pass the previously set tag from a ReadTagged call.
	// For example:
	// var tag any
	// n, err := conn.ReadTagged(buf, &tag)
	// if err != nil {
	//     // handle error
	// }
	// // process buf[:n] and tag as needed
	// _, err = conn.WriteTagged(responseBuf, tag)
	// if err != nil {
	//     // handle error
	// }
	WriteTagged([]byte, any) (int, error)

	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}
