package config

const (
	defaultIPv6Host          = "::"
	defaultUDPSessionIdleSec = 60
	defaultUDPMaxSession     = 1024
)

func DefaultIPv6HTTPHost() string       { return defaultIPv6Host }
func DefaultIPv6ControlHost() string    { return defaultIPv6Host }
func DefaultIPv6DataHost() string       { return defaultIPv6Host }
func DefaultIPv6TunnelHost() string     { return defaultIPv6Host }
func DefaultUDPSessionIdleSeconds() int { return defaultUDPSessionIdleSec }
func DefaultUDPMaxSessions() int        { return defaultUDPMaxSession }
