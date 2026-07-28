package auth

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	saml2 "github.com/russellhaering/gosaml2"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * SAML 2.0, service-provider initiated, HTTP-Redirect out and HTTP-POST back.
 *
 * KubeMG is the SP. It sends an unsigned AuthnRequest — signing one requires an
 * SP key pair every operator would have to generate and register, and it buys
 * nothing here because the *response* is what carries the identity and that is
 * signed by the IdP and verified below.
 *
 * The security of the whole exchange rests on two things: the assertion's
 * signature is checked against the certificates in the IdP's own metadata, and
 * the audience in it must be KubeMG's entity ID. Skipping either turns the ACS
 * endpoint into "post me any XML and I will believe it", so neither is optional
 * and neither is configurable.
 */

const (
	// samlMetadataTTL is how long fetched IdP metadata is reused.
	samlMetadataTTL = 15 * time.Minute
	// maxSAMLMetadata bounds a metadata fetch. Real documents are a few kilobytes;
	// this is what stops a misconfigured URL pointing at something enormous.
	maxSAMLMetadata = 2 << 20
	// bindingRedirect is the binding KubeMG sends its AuthnRequest over.
	bindingRedirect = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect"
	bindingPOST     = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST"
	nameIDFormat    = "urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified"
)

// SAMLClient is one configured IdP, ready to start and finish a sign-in.
type SAMLClient struct {
	sp     *saml2.SAMLServiceProvider
	config *db.SSOProviderConfig
}

// idpMetadata is the part of an IdP's metadata KubeMG needs: where to send
// people, and which certificates their assertions must be signed with.
//
// These are hand-rolled rather than taken from the SAML library's own types
// because a metadata document in the wild is as likely to be an EntitiesDescriptor
// holding several entities as a bare EntityDescriptor, and because a validUntil
// attribute in a format Go's time parser does not accept must not be the reason
// a whole login flow refuses to start.
type idpMetadata struct {
	SSOURL string
	Issuer string
	Certs  []*x509.Certificate
}

type entitiesDescriptorXML struct {
	XMLName  xml.Name              `xml:"EntitiesDescriptor"`
	Entities []entityDescriptorXML `xml:"EntityDescriptor"`
}

type entityDescriptorXML struct {
	XMLName          xml.Name              `xml:"EntityDescriptor"`
	EntityID         string                `xml:"entityID,attr"`
	IDPSSODescriptor *idpSSODescriptorXML  `xml:"IDPSSODescriptor"`
	Entities         []entityDescriptorXML `xml:"EntityDescriptor"`
}

type idpSSODescriptorXML struct {
	KeyDescriptors       []keyDescriptorXML `xml:"KeyDescriptor"`
	SingleSignOnServices []struct {
		Binding  string `xml:"Binding,attr"`
		Location string `xml:"Location,attr"`
	} `xml:"SingleSignOnService"`
}

type keyDescriptorXML struct {
	Use     string `xml:"use,attr"`
	KeyInfo struct {
		X509Data struct {
			Certificates []string `xml:"X509Certificate"`
		} `xml:"X509Data"`
	} `xml:"KeyInfo"`
}

type cachedMetadata struct {
	metadata *idpMetadata
	at       time.Time
}

var (
	metadataMu    sync.Mutex
	metadataCache = map[string]cachedMetadata{}
)

// NewSAMLClient prepares an IdP for use. acsURL is the endpoint the IdP posts
// its response to and entityID is what KubeMG calls itself; both are also what
// the generated SP metadata publishes, so the three cannot drift apart.
func NewSAMLClient(
	ctx context.Context, config *db.SSOProviderConfig, acsURL, entityID string,
) (*SAMLClient, error) {
	if config == nil || config.Protocol != db.ProtocolSAML {
		return nil, errors.New("provider is not a SAML provider")
	}

	metadata, err := loadIDPMetadata(ctx, config)
	if err != nil {
		return nil, err
	}

	store := &dsig.MemoryX509CertificateStore{Roots: metadata.Certs}
	return &SAMLClient{
		config: config,
		sp: &saml2.SAMLServiceProvider{
			IdentityProviderSSOURL:      metadata.SSOURL,
			IdentityProviderIssuer:      metadata.Issuer,
			AssertionConsumerServiceURL: acsURL,
			ServiceProviderIssuer:       entityID,
			// The audience is the SP's entity ID: an assertion minted for some
			// other service must not be replayable here.
			AudienceURI:         entityID,
			IDPCertificateStore: store,
			SignAuthnRequests:   false,
			NameIdFormat:        nameIDFormat,
		},
	}, nil
}

// AuthURL builds the redirect that starts a sign-in. relayState is echoed back
// by the IdP and is how the callback finds the request it belongs to.
func (c *SAMLClient) AuthURL(relayState string) (string, error) {
	url, err := c.sp.BuildAuthURL(relayState)
	if err != nil {
		return "", fmt.Errorf("build SAML request: %w", err)
	}
	return url, nil
}

// Assertion verifies a posted SAMLResponse and reads the identity out of it.
func (c *SAMLClient) Assertion(encodedResponse string) (db.SSOIdentity, error) {
	info, err := c.sp.RetrieveAssertionInfo(encodedResponse)
	if err != nil {
		return db.SSOIdentity{}, fmt.Errorf("verify SAML assertion: %w", err)
	}
	if info.WarningInfo != nil {
		switch {
		case info.WarningInfo.InvalidTime:
			return db.SSOIdentity{}, errors.New("the SAML assertion is expired or not yet valid")
		case info.WarningInfo.NotInAudience:
			return db.SSOIdentity{}, errors.New("the SAML assertion was issued for a different service")
		}
	}

	// A SAML attribute list is a different shape from a claim set, so it is
	// flattened into one before the shared claim readers see it — every value of
	// every attribute, keyed by both its name and its friendly name, since IdPs
	// are configured to send one or the other and an operator rarely knows which.
	claims := map[string]any{}
	for name, attribute := range info.Values {
		values := make([]string, 0, len(attribute.Values))
		for _, value := range attribute.Values {
			if trimmed := strings.TrimSpace(value.Value); trimmed != "" {
				values = append(values, trimmed)
			}
		}
		if len(values) == 0 {
			continue
		}
		claims[name] = values
		if friendly := strings.TrimSpace(attribute.FriendlyName); friendly != "" {
			if _, taken := claims[friendly]; !taken {
				claims[friendly] = values
			}
		}
	}

	username := claimString(claims, c.config.UsernameClaim, samlUsernameCandidates)
	if username == "" {
		username = strings.TrimSpace(info.NameID)
	}
	if username == "" {
		return db.SSOIdentity{}, errors.New("the SAML assertion carries no username")
	}

	return db.SSOIdentity{
		// The NameID is the IdP's own handle for this person; an assertion
		// without one falls back to the username it did send.
		ExternalID: strings.TrimSpace(info.NameID),
		Username:   username,
		Email:      claimString(claims, c.config.EmailClaim, samlEmailCandidates),
		Groups:     dedupe(claimStrings(claims, c.config.GroupsClaim, samlGroupCandidates)),
	}, nil
}

// SPMetadata renders KubeMG's own SAML metadata: what an operator uploads into
// their IdP to register this service.
//
// It is generated rather than typed out because the two things it contains —
// the entity ID and the assertion consumer URL — are the two an operator gets
// wrong by hand, and a mismatch between them and what the ACS endpoint actually
// enforces fails at the audience check with nothing to explain it.
func SPMetadata(entityID, acsURL string) ([]byte, error) {
	if entityID == "" || acsURL == "" {
		return nil, errors.New("SP metadata needs an entity ID and an ACS URL")
	}

	type assertionConsumerService struct {
		XMLName  xml.Name `xml:"md:AssertionConsumerService"`
		Binding  string   `xml:"Binding,attr"`
		Location string   `xml:"Location,attr"`
		Index    int      `xml:"index,attr"`
		Default  bool     `xml:"isDefault,attr"`
	}
	type spssoDescriptor struct {
		XMLName                    xml.Name `xml:"md:SPSSODescriptor"`
		ProtocolSupportEnumeration string   `xml:"protocolSupportEnumeration,attr"`
		AuthnRequestsSigned        bool     `xml:"AuthnRequestsSigned,attr"`
		WantAssertionsSigned       bool     `xml:"WantAssertionsSigned,attr"`
		NameIDFormat               string   `xml:"md:NameIDFormat"`
		ACS                        assertionConsumerService
	}
	type entityDescriptor struct {
		XMLName   xml.Name `xml:"md:EntityDescriptor"`
		Namespace string   `xml:"xmlns:md,attr"`
		EntityID  string   `xml:"entityID,attr"`
		SPSSO     spssoDescriptor
	}

	doc := entityDescriptor{
		Namespace: "urn:oasis:names:tc:SAML:2.0:metadata",
		EntityID:  entityID,
		SPSSO: spssoDescriptor{
			ProtocolSupportEnumeration: "urn:oasis:names:tc:SAML:2.0:protocol",
			AuthnRequestsSigned:        false,
			// The one thing KubeMG genuinely requires of the IdP, and the reason
			// the ACS endpoint can be trusted at all.
			WantAssertionsSigned: true,
			NameIDFormat:         nameIDFormat,
			ACS: assertionConsumerService{
				Binding:  bindingPOST,
				Location: acsURL,
				Index:    0,
				Default:  true,
			},
		},
	}

	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render SP metadata: %w", err)
	}
	return append([]byte(xml.Header), body...), nil
}

// CheckSAML reads a provider's IdP metadata and reports what it found, so an
// operator learns about an unreachable URL or a document with no signing
// certificate when they save the configuration rather than at someone's first
// sign-in.
func CheckSAML(ctx context.Context, config *db.SSOProviderConfig) (string, error) {
	metadata, err := loadIDPMetadataUncached(ctx, config)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Metadata read; sign-on endpoint %s, %d signing certificate(s)",
		metadata.SSOURL, len(metadata.Certs),
	), nil
}

func loadIDPMetadata(ctx context.Context, config *db.SSOProviderConfig) (*idpMetadata, error) {
	key := fmt.Sprintf("%d|%s", config.ID, config.SAMLMetadataURL)

	metadataMu.Lock()
	cached, ok := metadataCache[key]
	metadataMu.Unlock()
	if ok && time.Since(cached.at) < samlMetadataTTL {
		return cached.metadata, nil
	}

	metadata, err := loadIDPMetadataUncached(ctx, config)
	if err != nil {
		return nil, err
	}

	metadataMu.Lock()
	metadataCache[key] = cachedMetadata{metadata: metadata, at: time.Now()}
	metadataMu.Unlock()
	return metadata, nil
}

func loadIDPMetadataUncached(ctx context.Context, config *db.SSOProviderConfig) (*idpMetadata, error) {
	raw := strings.TrimSpace(config.SAMLMetadataXML)
	if raw == "" {
		if strings.TrimSpace(config.SAMLMetadataURL) == "" {
			return nil, errors.New("this provider has neither metadata XML nor a metadata URL")
		}
		fetched, err := fetchMetadata(ctx, config.SAMLMetadataURL)
		if err != nil {
			return nil, err
		}
		raw = fetched
	}
	return parseIDPMetadata(raw)
}

func fetchMetadata(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("read IdP metadata: %w", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("read IdP metadata: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the metadata URL answered %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxSAMLMetadata))
	if err != nil {
		return "", fmt.Errorf("read IdP metadata: %w", err)
	}
	return string(body), nil
}

// parseIDPMetadata pulls the sign-on endpoint and the signing certificates out
// of a metadata document, accepting either a single entity or a federation's
// worth of them.
func parseIDPMetadata(raw string) (*idpMetadata, error) {
	entities := []entityDescriptorXML{}

	var single entityDescriptorXML
	if err := xml.Unmarshal([]byte(raw), &single); err == nil && single.XMLName.Local == "EntityDescriptor" {
		entities = append(entities, single)
		entities = append(entities, single.Entities...)
	} else {
		var group entitiesDescriptorXML
		if err := xml.Unmarshal([]byte(raw), &group); err != nil {
			return nil, fmt.Errorf("parse IdP metadata: %w", err)
		}
		entities = append(entities, group.Entities...)
	}

	for _, entity := range entities {
		descriptor := entity.IDPSSODescriptor
		if descriptor == nil {
			continue
		}

		metadata := &idpMetadata{Issuer: strings.TrimSpace(entity.EntityID)}
		for _, service := range descriptor.SingleSignOnServices {
			if service.Binding == bindingRedirect && service.Location != "" {
				metadata.SSOURL = strings.TrimSpace(service.Location)
				break
			}
		}
		if metadata.SSOURL == "" {
			continue
		}

		for _, key := range descriptor.KeyDescriptors {
			// An empty use means the key serves both purposes, which is what
			// most IdPs publish; only an explicitly encryption-only key is
			// skipped, since verifying against it would never succeed.
			if key.Use == "encryption" {
				continue
			}
			for _, encoded := range key.KeyInfo.X509Data.Certificates {
				cert, err := parseMetadataCertificate(encoded)
				if err != nil {
					return nil, err
				}
				metadata.Certs = append(metadata.Certs, cert)
			}
		}
		if len(metadata.Certs) == 0 {
			return nil, errors.New("the IdP metadata publishes no signing certificate")
		}
		return metadata, nil
	}
	return nil, errors.New("the metadata document describes no SAML identity provider")
}

func parseMetadataCertificate(encoded string) (*x509.Certificate, error) {
	// Metadata carries a bare base64 body rather than PEM, and pretty-printed
	// documents wrap it across lines.
	cleaned := strings.NewReplacer("\n", "", "\r", "", "\t", "", " ", "").Replace(encoded)
	der, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("decode IdP certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse IdP certificate: %w", err)
	}
	return cert, nil
}
