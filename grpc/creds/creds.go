package creds

import (
	"crypto/tls"
	"fmt"
	"net"

	"golang.org/x/net/context"
	"google.golang.org/grpc/credentials"
)

// clientTransportCredentials is a grpc/credentials.TransportCredentials which supports
// connecting to, and verifying multiple DNS names
type clientTransportCredentials struct {
	clientConfig *tls.Config
}

// New returns a new initialized grpc/credentials.TransportCredentials
func NewClientTransport(clientConfig *tls.Config) credentials.TransportCredentials {
	return &clientTransportCredentials{clientConfig}
}

// ClientHandshake performs the TLS handshake for a client -> server connection
func (tc *clientTransportCredentials) ClientHandshake(ctx context.Context, addr string, rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, err
	}
	// We need to set the `ServerName` attribute for the tls.Config. Since we
	// can't modify the existing `tc.clientConfig` we create a new one and port over
	// the few fields we were using the `clientConfig` as a container for.
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS12, // Override default of tls.VersionTLS10
		MaxVersion:   tls.VersionTLS12, // Same as default in golang <= 1.6
		ServerName:   host,
		RootCAs:      tc.clientConfig.RootCAs,
		Certificates: tc.clientConfig.Certificates,
	}
	conn := tls.Client(rawConn, tlsConfig)
	errChan := make(chan error, 1)
	go func() {
		errChan <- conn.Handshake()
	}()
	select {
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("boulder/grpc/creds: %s", ctx.Err())
	case err := <-errChan:
		if err != nil {
			_ = rawConn.Close()
			return nil, nil, fmt.Errorf("boulder/grpc/creds: TLS handshake failed: %s", err)
		}
		return conn, nil, nil
	}
}

//ServerHandshake performs the TLS handshake for a server <- client connection
func (tc *clientTransportCredentials) ServerHandshake(rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, fmt.Errorf(
		"boulder/grpc/creds: Server-side handshakes are not implemented with " +
			"clientTransportCredentials")
}

// Info returns information about the transport protocol used
func (tc *clientTransportCredentials) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{
		SecurityProtocol: "tls",
		SecurityVersion:  "1.2", // We *only* support TLS 1.2
	}
}

// GetRequestMetadata returns nil, nil since TLS credentials do not have metadata.
func (tc *clientTransportCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return nil, nil
}

// RequireTransportSecurity always returns true because TLS is transport security
func (tc *clientTransportCredentials) RequireTransportSecurity() bool {
	return true
}

// clientTransportCredentials is a grpc/credentials.TransportCredentials which supports
// filtering acceptable peers by client certificate SAN.
type serverTransportCredentials struct {
	serverConfig *tls.Config
	whitelist    map[string]struct{}
}

func NewServerTransport(serverConfig *tls.Config, whitelist map[string]struct{}) credentials.TransportCredentials {
	return &serverTransportCredentials{serverConfig, whitelist}
}

func (tc *serverTransportCredentials) peerIsWhitelisted(peerState tls.ConnectionState) error {
	// If there's no whitelist, all clients are OK
	if tc.whitelist == nil {
		return nil
	}

	// Otherwise its time to start inspecting the peer's `VerifiedChains`
	chains := peerState.VerifiedChains
	if len(chains) < 1 {
		return fmt.Errorf("boulder/grpc/creds: peer had zero VerifiedChains")
	}

	/*
	 * For each of the peer's verified chains we can look at the chain's leaf
	 * certificate and check whether the subject common name is in the whitelist.
	 * At least one chain must have a leaf certificate with a subject CN that
	 * matches the whitelist
	 *
	 * Its important we process `VerifiedChains` instead of processing
	 * `PeerCertificates` to ensure that we match the subject CN of the
	 * leaf certificate that was verified in `conn.Handshake()`. To do otherwise
	 * would allow an attacker to include a whitelisted certificate in
	 * `PeerCertificates` that matched the whitelist but wasn't used in the chain
	 * the server validated.
	 */
	var whitelisted bool
	for _, chain := range chains {
		leafSubjectCN := chain[0].Subject.CommonName
		if _, ok := tc.whitelist[leafSubjectCN]; ok {
			whitelisted = true
		}
	}

	// If none of the peer's validated chains had a leaf certificate with a
	// whitelisted CN then we have to reject the connection
	if !whitelisted {
		return fmt.Errorf(
			"boulder/grpc/creds: peer's verified TLS chains did not include a leaf " +
				"certificate with a whitelisted subject CN")
	}

	// Otherwise, the peer is whitelisted! Come on in!
	return nil
}

// ServerHandshake performs the TLS handshake for a server <- client connection
func (tc *serverTransportCredentials) ServerHandshake(rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	if tc.serverConfig == nil {
		return nil, nil, fmt.Errorf("boulder/grpc/creds: `serverConfig` must not be nil")
	}

	// Perform the server <- client TLS handshake
	conn := tls.Server(rawConn, tc.serverConfig)
	if err := conn.Handshake(); err != nil {
		return nil, nil, err
	}

	// If the peer isn't whitelisted, abort and return an error
	if err := tc.peerIsWhitelisted(conn.ConnectionState()); err != nil {
		return nil, nil, err
	}

	return conn, credentials.TLSInfo{conn.ConnectionState()}, nil
}

func (tc *serverTransportCredentials) ClientHandshake(ctx context.Context, addr string, rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, fmt.Errorf(
		"boulder/grpc/creds: Client-side handshakes are not implemented with " +
			"serverTransportCredentials")
}

func (tc *serverTransportCredentials) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{
		SecurityProtocol: "tls",
		SecurityVersion:  "1.2", // We *only* support TLS 1.2
	}
}

// GetRequestMetadata returns nil, nil since TLS credentials do not have metadata.
func (tc *serverTransportCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return nil, nil
}

// RequireTransportSecurity always returns true because TLS is transport security
func (tc *serverTransportCredentials) RequireTransportSecurity() bool {
	return true
}
