package main

import (
	"net"
	"strings"
)

// IsTailnetIP checks if the given IP address is in the Tailscale CGNAT range (100.64.0.0/10)
// This range spans 100.64.0.0 - 100.127.255.255
func IsTailnetIP(ipStr string) bool {
	// Handle IPv6-mapped IPv4 addresses and port suffixes
	ipStr = cleanIPString(ipStr)

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	// Convert to IPv4 if needed
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}

	// Check if in 100.64.0.0/10 range
	// First octet must be 100
	// Second octet must be 64-127 (bits 01xxxxxx)
	return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
}

// cleanIPString extracts the IP address from various formats
func cleanIPString(ipStr string) string {
	// Remove port if present (handle both IPv4 "ip:port" and IPv6 "[ip]:port")
	if strings.HasPrefix(ipStr, "[") {
		// IPv6 format: [::1]:port or [ip]:port
		if idx := strings.LastIndex(ipStr, "]:"); idx != -1 {
			ipStr = ipStr[1:idx]
		} else if strings.HasSuffix(ipStr, "]") {
			ipStr = ipStr[1 : len(ipStr)-1]
		}
	} else if strings.Contains(ipStr, ":") {
		// Could be IPv4:port or IPv6 address
		// Check if it's IPv4:port by counting colons
		if strings.Count(ipStr, ":") == 1 {
			// IPv4 with port
			if idx := strings.LastIndex(ipStr, ":"); idx != -1 {
				ipStr = ipStr[:idx]
			}
		}
		// Otherwise it's IPv6 without port, leave as is
	}

	return ipStr
}

// GetClientIP extracts the real client IP from request headers or remote address
func GetClientIP(remoteAddr string, xForwardedFor string, xRealIP string) string {
	// Prefer X-Real-IP if set (typically set by nginx)
	if xRealIP != "" {
		return cleanIPString(xRealIP)
	}

	// Fall back to X-Forwarded-For (first IP in chain)
	if xForwardedFor != "" {
		ips := strings.Split(xForwardedFor, ",")
		if len(ips) > 0 {
			return cleanIPString(strings.TrimSpace(ips[0]))
		}
	}

	// Fall back to RemoteAddr
	return cleanIPString(remoteAddr)
}
