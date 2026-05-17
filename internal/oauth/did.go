// Package oauth provides AT Protocol OAuth implementation.
// DID resolution for did:plc and did:web methods.
package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultPLCDirectoryURL is the default PLC directory URL.
const DefaultPLCDirectoryURL = "https://plc.directory"

// DIDDocument represents a DID document.
type DIDDocument struct {
	ID                 string               `json:"id"`
	AlsoKnownAs        []string             `json:"alsoKnownAs,omitempty"`
	Service            []Service            `json:"service,omitempty"`
	VerificationMethod []VerificationMethod `json:"verificationMethod,omitempty"`
}

// VerificationMethod is one entry in a DID document's verificationMethod
// array. AT Protocol uses the W3C DID Core `Multikey` type to carry
// signing keys in `publicKeyMultibase` form. The `#atproto` suffix on
// `ID` identifies the general service-auth key; `#atproto_label`
// identifies a labeler key.
type VerificationMethod struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Controller         string `json:"controller"`
	PublicKeyMultibase string `json:"publicKeyMultibase,omitempty"`
}

// Service represents a service in a DID document.
type Service struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint string `json:"serviceEndpoint"`
}

// DIDResolver handles DID resolution.
type DIDResolver struct {
	plcDirectoryURL string
	httpClient      *http.Client
}

// DIDResolverOption configures the DID resolver.
type DIDResolverOption func(*DIDResolver)

// WithPLCDirectoryURL sets the PLC directory URL.
func WithPLCDirectoryURL(plcURL string) DIDResolverOption {
	return func(r *DIDResolver) {
		r.plcDirectoryURL = plcURL
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) DIDResolverOption {
	return func(r *DIDResolver) {
		r.httpClient = client
	}
}

// maxDIDDocumentBytes is the largest DID document or handle-resolution
// response we will accept. Real DID docs are a few KB; the cap guards
// against a hostile PLC or did:web returning multi-GB JSON and
// exhausting memory in io.ReadAll.
const maxDIDDocumentBytes = 256 * 1024

// NewDIDResolverWithPLC builds a DIDResolver, applying the PLC
// directory URL override when non-empty. Common case helper for
// callers that just need to thread cfg.PLCDirectoryURL through; tests
// and callers that need WithHTTPClient should call NewDIDResolver
// directly with the option list.
func NewDIDResolverWithPLC(plcDirectoryURL string) *DIDResolver {
	if plcDirectoryURL == "" {
		return NewDIDResolver()
	}
	return NewDIDResolver(WithPLCDirectoryURL(plcDirectoryURL))
}

// NewDIDResolver creates a new DID resolver.
func NewDIDResolver(opts ...DIDResolverOption) *DIDResolver {
	r := &DIDResolver{
		plcDirectoryURL: DefaultPLCDirectoryURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			// Reject redirects that would land on a private /
			// loopback / link-local address. This closes the
			// SSRF hole where a hostile PLC directory redirects
			// a lookup to 127.0.0.1 or 169.254.169.254 after the
			// initial hostname check has already passed.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				if err := rejectPrivateHost(req.URL.Host); err != nil {
					return fmt.Errorf("redirect blocked: %w", err)
				}
				return nil
			},
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// readBounded reads at most maxDIDDocumentBytes from r and returns an
// error if the stream is larger. Replaces io.ReadAll on DID-adjacent
// HTTP responses.
func readBounded(r io.Reader) ([]byte, error) {
	limited := io.LimitReader(r, maxDIDDocumentBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxDIDDocumentBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxDIDDocumentBytes)
	}
	return body, nil
}

// ResolveDID resolves a DID to its document.
// Supports did:plc and did:web methods.
func (r *DIDResolver) ResolveDID(did string) (*DIDDocument, error) {
	switch {
	case strings.HasPrefix(did, "did:plc:"):
		return r.resolvePLCDID(did)
	case strings.HasPrefix(did, "did:web:"):
		return r.resolveWebDID(did)
	default:
		return nil, fmt.Errorf("unsupported DID method: %s", did)
	}
}

// resolvePLCDID resolves a did:plc DID using the PLC directory.
func (r *DIDResolver) resolvePLCDID(did string) (*DIDDocument, error) {
	resolveURL := r.plcDirectoryURL + "/" + did

	resp, err := r.httpClient.Get(resolveURL)
	if err != nil {
		return nil, fmt.Errorf("PLC request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("DID not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("PLC resolution failed with status %d", resp.StatusCode)
	}

	body, err := readBounded(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return parseDIDDocument(body, did)
}

// resolveWebDID resolves a did:web DID.
//
// Security: did:web documents are fetched from the hostname embedded in
// the DID, which is attacker-controlled. To prevent SSRF we reject any
// hostname that resolves to a loopback, link-local, private, or
// unspecified address before making the request. An operator that
// legitimately needs to resolve an internal did:web can set
// ALLOW_PRIVATE_DID_WEB=true at their own risk (not implemented
// here; the cleaner path is to run a split-horizon PLC directory).
func (r *DIDResolver) resolveWebDID(did string) (*DIDDocument, error) {
	// Extract domain from did:web:domain
	// Handle percent-encoded colons for ports (did:web:localhost%3A3000)
	domain := strings.TrimPrefix(did, "did:web:")
	domain, err := url.PathUnescape(domain)
	if err != nil {
		return nil, fmt.Errorf("invalid did:web encoding: %w", err)
	}

	if err := rejectPrivateHost(domain); err != nil {
		return nil, fmt.Errorf("did:web hostname rejected: %w", err)
	}

	// Build the .well-known URL
	didURL := "https://" + domain + "/.well-known/did.json"

	resp, err := r.httpClient.Get(didURL)
	if err != nil {
		return nil, fmt.Errorf("web DID request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("DID not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web DID resolution failed with status %d", resp.StatusCode)
	}

	body, err := readBounded(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return parseDIDDocument(body, did)
}

// ResolveHandleToDID resolves a handle to a DID using the AT Protocol identity resolution.
func (r *DIDResolver) ResolveHandleToDID(handle string) (string, error) {
	// Use the standard AT Protocol handle resolution endpoint
	resolveURL := "https://bsky.social/xrpc/com.atproto.identity.resolveHandle?handle=" + url.QueryEscape(handle)

	resp, err := r.httpClient.Get(resolveURL)
	if err != nil {
		return "", fmt.Errorf("handle resolution request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("handle resolution failed with status %d", resp.StatusCode)
	}

	body, err := readBounded(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		DID string `json:"did"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if result.DID == "" {
		return "", errors.New("no DID in response")
	}

	return result.DID, nil
}

// GetPDSEndpoint extracts the PDS endpoint from a DID document.
func (doc *DIDDocument) GetPDSEndpoint() string {
	for _, svc := range doc.Service {
		if svc.Type == "AtprotoPersonalDataServer" {
			return svc.ServiceEndpoint
		}
	}
	return ""
}

// AtprotoSigningKey returns the #atproto verification method used for
// service-auth JWT signature verification. Only entries with
// `type == "Multikey"` are accepted — legacy Ecdsa* types are rejected
// to avoid parsing ambiguity. Returns nil if no matching method exists.
func (doc *DIDDocument) AtprotoSigningKey() *VerificationMethod {
	for i := range doc.VerificationMethod {
		vm := &doc.VerificationMethod[i]
		if vm.Type != "Multikey" {
			continue
		}
		// The id is expected to be "<did>#atproto" or just "#atproto";
		// accept either form (the `#atproto` suffix is the canonical
		// selector per the ATProto identity spec).
		if vm.ID == "#atproto" || endsWithFragment(vm.ID, "#atproto") {
			return vm
		}
	}
	return nil
}

func endsWithFragment(id, frag string) bool {
	return len(id) >= len(frag) && id[len(id)-len(frag):] == frag
}

// GetLabelerEndpoint extracts the AtprotoLabeler service endpoint from a DID
// document. Labelers advertise this service separately from the PDS; it is
// the host whose /xrpc/com.atproto.label.subscribeLabels and queryLabels
// endpoints serve label data. Some moderation services colocate the labeler
// on the same host as their PDS; others run it standalone.
func (doc *DIDDocument) GetLabelerEndpoint() string {
	for _, svc := range doc.Service {
		if svc.Type == "AtprotoLabeler" {
			return svc.ServiceEndpoint
		}
	}
	return ""
}

// GetHandle extracts the handle from a DID document's alsoKnownAs field.
// Returns the first at:// URI stripped of the at:// prefix.
//
// SECURITY: the value returned here is *not* verified. A DID document
// is signed by the DID subject, so the subject can claim any handle
// they want in alsoKnownAs. Callers that rely on a handle for a
// *trust* decision (access control, permissioning, impersonation
// checks) must independently resolve the handle back to the DID via
// com.atproto.identity.resolveHandle and assert the result matches.
// Callers that only use the handle for display purposes (e.g.
// persisting it on an actor row for UI rendering) can use this as-is.
// The current call sites are display-only.
func (doc *DIDDocument) GetHandle() string {
	for _, aka := range doc.AlsoKnownAs {
		if strings.HasPrefix(aka, "at://") {
			return strings.TrimPrefix(aka, "at://")
		}
	}
	return ""
}

// parseDIDDocument parses a DID document from JSON.
// If expectedDID is non-empty, the document's id must match it.
func parseDIDDocument(data []byte, expectedDID ...string) (*DIDDocument, error) {
	var doc DIDDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse DID document: %w", err)
	}
	if doc.ID == "" {
		return nil, errors.New("invalid DID document: missing id")
	}
	if len(expectedDID) > 0 && expectedDID[0] != "" && doc.ID != expectedDID[0] {
		return nil, fmt.Errorf("DID document id %q does not match requested %q", doc.ID, expectedDID[0])
	}
	return &doc, nil
}

// rejectPrivateHost errors out if host resolves to any loopback,
// link-local, private, unspecified, or multicast address. Used to
// block SSRF via attacker-controlled hostnames (did:web resolution).
// Strips any ":port" suffix before resolving.
func rejectPrivateHost(host string) error {
	// strip port if present (host may be "example.com:8443")
	hostOnly := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostOnly = h
	}
	// Hostnames that are literally loopback-ish get rejected early so
	// that even broken resolvers can't trick us.
	lower := strings.ToLower(hostOnly)
	if lower == "localhost" || lower == "localhost.localdomain" {
		return fmt.Errorf("loopback hostname %q not allowed", host)
	}

	ips, err := net.LookupIP(hostOnly)
	if err != nil {
		// Resolution failure is treated as rejection — an unreachable
		// host can't serve a DID document anyway, and proceeding would
		// either fail or hit a different IP on retry.
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("resolve %q: no addresses", host)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
			return fmt.Errorf("hostname %q resolves to disallowed address %s", host, ip)
		}
	}
	return nil
}

// IsDIDPLC checks if a DID uses the plc method.
func IsDIDPLC(did string) bool {
	return strings.HasPrefix(did, "did:plc:")
}

// IsDIDWeb checks if a DID uses the web method.
func IsDIDWeb(did string) bool {
	return strings.HasPrefix(did, "did:web:")
}
