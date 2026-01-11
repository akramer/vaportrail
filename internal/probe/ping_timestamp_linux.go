//go:build linux

package probe

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// Timestamp socket option constants for Linux
const (
	soTimestampNS = unix.SO_TIMESTAMPNS // 35 on Linux, nanosecond precision
)

var (
	timestampFallbackOnce sync.Once
	useTXTimestamp        bool
	txTimestampOnce       sync.Once
)

// readWithKernelTimestamp reads ICMP replies and extracts kernel receive timestamps on Linux
// If TX timestamps are enabled, it retrieves both send and receive kernel timestamps
func readWithKernelTimestamp(conn *icmp.PacketConn, dst *net.IPAddr, id, seq int, start time.Time) (float64, error) {
	// Get the raw file descriptor
	var fd int
	if pc := conn.IPv4PacketConn(); pc != nil {
		if sc, ok := pc.PacketConn.(interface {
			SyscallConn() (syscall.RawConn, error)
		}); ok {
			if rawConn, err := sc.SyscallConn(); err == nil {
				rawConn.Control(func(fdPtr uintptr) {
					fd = int(fdPtr)
				})
			}
		}
	}

	if fd == 0 {
		log.Println("Ping probe: WARNING - failed to get file descriptor, falling back to userspace timing")
		return fallbackToUserspace(conn, dst, id, seq, start)
	}

	// Try to enable TX timestamping (once per process)
	txTimestampOnce.Do(func() {
		// Enable SO_TIMESTAMPING with TX and RX software timestamps
		// SOF_TIMESTAMPING_TX_SOFTWARE = 0x2  - Get TX timestamp
		// SOF_TIMESTAMPING_RX_SOFTWARE = 0x8  - Get RX timestamp
		// SOF_TIMESTAMPING_SOFTWARE = 0x10   - Enable software timestamping
		// SOF_TIMESTAMPING_OPT_CMSG = 0x400  - Report timestamps via cmsg
		// SOF_TIMESTAMPING_OPT_TSONLY = 0x800 - Only timestamp, not the full packet
		flags := unix.SOF_TIMESTAMPING_TX_SOFTWARE |
			unix.SOF_TIMESTAMPING_RX_SOFTWARE |
			unix.SOF_TIMESTAMPING_SOFTWARE |
			unix.SOF_TIMESTAMPING_OPT_CMSG |
			unix.SOF_TIMESTAMPING_OPT_TSONLY

		err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, flags)
		if err == nil {
			useTXTimestamp = true
			log.Println("Ping probe: using TX+RX kernel timestamps (SO_TIMESTAMPING)")
		} else {
			log.Printf("Ping probe: SO_TIMESTAMPING not available (%v), using RX-only timestamps", err)
		}
	})

	// If TX timestamping is enabled, use the new path
	if useTXTimestamp {
		return readWithTXTimestamp(fd, conn, dst, id, seq, start)
	}

	// Fall back to RX-only timestamps
	return readWithRXOnlyTimestamp(fd, conn, dst, id, seq, start)
}

// readWithTXTimestamp retrieves TX timestamp from error queue and RX timestamp from data path
func readWithTXTimestamp(fd int, conn *icmp.PacketConn, dst *net.IPAddr, id, seq int, sendTime time.Time) (float64, error) {
	// Buffer for packet data
	buf := make([]byte, 1500)
	// Buffer for control messages (out-of-band data)
	oob := make([]byte, 256)

	// First, poll error queue for TX timestamp with a short timeout
	// The TX timestamp is delivered to the error queue after the packet is sent
	var txTimestamp time.Time
	gotTX := false

	// Try to get TX timestamp from error queue (non-blocking with short poll)
	for i := 0; i < 10 && !gotTX; i++ {
		n, oobn, _, _, err := unix.Recvmsg(fd, buf, oob, unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				// No TX timestamp available yet, wait a bit
				time.Sleep(100 * time.Microsecond)
				continue
			}
			break // Other error, give up on TX timestamp
		}

		// Parse TX timestamp from control messages
		if oobn > 0 {
			scms, err := unix.ParseSocketControlMessage(oob[:oobn])
			if err == nil {
				for _, scm := range scms {
					if scm.Header.Level == unix.SOL_SOCKET && scm.Header.Type == unix.SCM_TIMESTAMPING {
						// SCM_TIMESTAMPING contains an array of 3 timespecs:
						// [0] = software timestamp, [1] = deprecated, [2] = hardware timestamp
						if len(scm.Data) >= 16 {
							sec := int64(binary.LittleEndian.Uint64(scm.Data[0:8]))
							nsec := int64(binary.LittleEndian.Uint64(scm.Data[8:16]))
							txTimestamp = time.Unix(sec, nsec)
							gotTX = true
						}
					}
				}
			}
		}
		_ = n // We don't need the error queue packet data
	}

	// Now read the actual ICMP reply with RX timestamp using poll for timeout
	// Use 5 second timeout (standard probe timeout)
	// The conn deadline is set but we can't easily access it, so use a reasonable default
	timeoutMs := 5000
	_ = conn // conn passed for potential future deadline access

	pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}

	for {
		// Poll with timeout
		n, err := unix.Poll(pollFds, timeoutMs)
		if err != nil {
			if err == unix.EINTR {
				continue // Interrupted, retry
			}
			return 0, fmt.Errorf("poll failed: %w", err)
		}
		if n == 0 {
			return 0, fmt.Errorf("timeout waiting for ICMP reply")
		}

		// Data is ready, read it
		msgN, oobn, _, from, err := unix.Recvmsg(fd, buf, oob, unix.MSG_DONTWAIT)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				continue // Spurious wakeup, retry poll
			}
			return 0, fmt.Errorf("failed to recvmsg: %w", err)
		}

		// Extract RX kernel timestamp from control messages
		var rxTimestamp time.Time
		gotRX := false

		if oobn > 0 {
			scms, err := unix.ParseSocketControlMessage(oob[:oobn])
			if err == nil {
				for _, scm := range scms {
					if scm.Header.Level == unix.SOL_SOCKET && scm.Header.Type == unix.SCM_TIMESTAMPING {
						if len(scm.Data) >= 16 {
							sec := int64(binary.LittleEndian.Uint64(scm.Data[0:8]))
							nsec := int64(binary.LittleEndian.Uint64(scm.Data[8:16]))
							rxTimestamp = time.Unix(sec, nsec)
							gotRX = true
						}
					} else if scm.Header.Level == unix.SOL_SOCKET && scm.Header.Type == unix.SCM_TIMESTAMPNS {
						// Fall back to SCM_TIMESTAMPNS if available
						if len(scm.Data) >= 16 {
							sec := int64(binary.LittleEndian.Uint64(scm.Data[0:8]))
							nsec := int64(binary.LittleEndian.Uint64(scm.Data[8:16]))
							rxTimestamp = time.Unix(sec, nsec)
							gotRX = true
						}
					}
				}
			}
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
		rm, err := icmp.ParseMessage(1, buf[:msgN])
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

			// Calculate RTT
			if gotTX && gotRX {
				// Best case: both kernel timestamps available
				elapsed := rxTimestamp.Sub(txTimestamp)
				return float64(elapsed.Nanoseconds()), nil
			} else if gotRX {
				// Only RX timestamp, use userspace sendTime
				elapsed := rxTimestamp.Sub(sendTime)
				return float64(elapsed.Nanoseconds()), nil
			} else {
				// No kernel timestamps, fall back to userspace
				return float64(time.Since(sendTime).Nanoseconds()), nil
			}
		case ipv4.ICMPTypeDestinationUnreachable:
			return 0, fmt.Errorf("destination unreachable")
		case ipv4.ICMPTypeTimeExceeded:
			return 0, fmt.Errorf("time exceeded (TTL expired)")
		default:
			continue
		}
	}
}

// readWithRXOnlyTimestamp reads ICMP replies with RX-only kernel timestamps (fallback path)
func readWithRXOnlyTimestamp(fd int, conn *icmp.PacketConn, dst *net.IPAddr, id, seq int, start time.Time) (float64, error) {
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
					if scm.Header.Level == unix.SOL_SOCKET && scm.Header.Type == unix.SCM_TIMESTAMPNS {
						// Parse Timespec (nanoseconds)
						if len(scm.Data) >= 16 {
							sec := int64(binary.LittleEndian.Uint64(scm.Data[0:8]))
							nsec := int64(binary.LittleEndian.Uint64(scm.Data[8:16]))
							kernelTime = time.Unix(sec, nsec)
							gotTimestamp = true
						}
					} else if scm.Header.Level == unix.SOL_SOCKET && scm.Header.Type == unix.SCM_TIMESTAMP {
						// Fallback to Timeval (microseconds)
						if len(scm.Data) >= 16 {
							sec := int64(binary.LittleEndian.Uint64(scm.Data[0:8]))
							usec := int64(binary.LittleEndian.Uint64(scm.Data[8:16]))
							kernelTime = time.Unix(sec, usec*1000)
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
