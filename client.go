package logos

import "github.com/tta-lab/temenos/client"

// newClient creates a commandRunner connected to a temenos daemon.
// addr formats:
//   - Empty string: resolve from TEMENOS_LISTEN_ADDR → TEMENOS_SOCKET_PATH → default socket
//   - Starts with "/" or ".": unix socket path
//   - Starts with "http://": HTTP base URL (TCP)
//   - Otherwise (e.g. ":8081", "localhost:8081"): TCP
func newClient(addr string) (commandRunner, error) {
	return client.New(addr)
}
