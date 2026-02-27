package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
)

// GetDeviceID reads the physical MAC address of the machine and hashes it
// so the client sees a clean, standard ID like "NINE-A1B2C3D4"
func GetDeviceID() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "UNKNOWN-DEVICE"
	}

	var macAddress string
	for _, i := range interfaces {
		// Find the first active physical network interface
		if i.Flags&net.FlagUp != 0 && len(i.HardwareAddr) > 0 {
			macAddress = i.HardwareAddr.String()
			break
		}
	}

	if macAddress == "" {
		return "UNKNOWN-DEVICE"
	}

	// Hash the MAC address for security and clean display
	hash := sha256.Sum256([]byte(macAddress + "NINE-POS-SALT"))
	hashString := hex.EncodeToString(hash[:])

	// Return a clean 8-character hardware ID
	return "NINE-" + strings.ToUpper(hashString[:8])
}
