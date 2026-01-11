//go:build darwin

package probe

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

var (
	timestampFallbackOnce sync.Once
)

// Note: macOS does NOT support send-side (TX) kernel timestamps.
// Only receive timestamps are available via SO_TIMESTAMP.
// This means there will always be ~60-100µs overhead compared to the ping command,
// which uses kernel-level timing on both send and receive.
// This limitation is inherent to the macOS kernel and cannot be worked around.

// readWithKernelTimestamp reads ICMP replies and extracts kernel receive timestamps on macOS
func readWithKernelTimestamp(conn *icmp.PacketConn, dst *net.IPAddr, id, seq int, start time.Time) (float64, error) {
	// Get the raw file descriptor
	var fd int
	if pc := conn.IPv4PacketConn(); pc != nil {
		if sc, ok := pc.PacketConn.(interface {
			SyscallConn() (interface{ Control(func(fd uintptr)) error }, error)
		}); ok {
			rawConn, err := sc.SyscallConn()
			if err == nil {
				rawConn.Control(func(fdPtr uintptr) {
					fd = int(fdPtr)
				})
			}
		}
	}

	if fd == 0 {
		return fallbackToUserspace(conn, dst, id, seq, start)
	}

	// Buffer for packet data
	buf := make([]byte, 1500)
	// Buffer for control messages (out-of-band data)
	oob := make([]byte, 128)

	for {
		n, oobn, _, from, err := unix.Recvmsg(fd, buf, oob, 0)
		if err != nil {
			return 0, fmt.Errorf("failed to recvmsg: %w", err)
		}

		// Extract kernel timestamp from control messages
		var kernelTime time.Time
		gotTimestamp := false

		if oobn > 0 {
			scms, err := unix.ParseSocketControlMessage(oob[:oobn])
			if err == nil {
				for _, scm := range scms {
					// macOS: SCM_TIMESTAMP with Timeval (microseconds)
					// SCM_TIMESTAMP on darwin = 0x02
					if scm.Header.Level == unix.SOL_SOCKET && scm.Header.Type == unix.SCM_TIMESTAMP {
						// macOS Timeval: Sec (int64), Usec (int32), plus padding
						// Total size is 16 bytes on 64-bit
						if len(scm.Data) >= 12 {
							sec := int64(binary.LittleEndian.Uint64(scm.Data[0:8]))
							usec := int32(binary.LittleEndian.Uint32(scm.Data[8:12]))
							kernelTime = time.Unix(sec, int64(usec)*1000)
							gotTimestamp = true
						}
					}
				}
			}
		}

		if !gotTimestamp {
			timestampFallbackOnce.Do(func() {
				log.Println("Ping probe: WARNING - kernel timestamp not received, falling back to userspace timing")
			})
			return fallbackToUserspace(conn, dst, id, seq, start)
		}

		// Validate source address
		var fromIP net.IP
		switch addr := from.(type) {
		case *unix.SockaddrInet4:
			fromIP = net.IP(addr.Addr[:])
		}
		if fromIP != nil && !fromIP.Equal(dst.IP) {
			continue
		}

		// Parse ICMP message
		rm, err := icmp.ParseMessage(1, buf[:n])
		if err != nil {
			return 0, fmt.Errorf("failed to parse ICMP reply: %w", err)
		}

		// Verify it's our echo reply
		switch rm.Type {
		case ipv4.ICMPTypeEchoReply:
			if echo, ok := rm.Body.(*icmp.Echo); ok {
				if echo.ID != id || echo.Seq != seq {
					continue
				}
			}
			// Calculate RTT using kernel timestamp
			elapsed := kernelTime.Sub(start)
			return float64(elapsed.Nanoseconds()), nil
		case ipv4.ICMPTypeDestinationUnreachable:
			return 0, fmt.Errorf("destination unreachable")
		case ipv4.ICMPTypeTimeExceeded:
			return 0, fmt.Errorf("time exceeded (TTL expired)")
		default:
			continue
		}
	}
}

// fallbackToUserspace handles the case when kernel timestamps aren't available
func fallbackToUserspace(conn *icmp.PacketConn, dst *net.IPAddr, id, seq int, start time.Time) (float64, error) {
	return readWithUserspaceTimestamp(conn, dst, id, seq, start)
}
