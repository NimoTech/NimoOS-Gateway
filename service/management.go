package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"go.uber.org/zap"
)

// uploadTransport is a custom transport optimized for large file uploads.
var uploadTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   20,
	IdleConnTimeout:       120 * time.Second,
	ResponseHeaderTimeout: 300 * time.Second,
	WriteBufferSize:       256 * 1024,
	ReadBufferSize:        256 * 1024,
}

const RoutesFile = "routes.json"

type Management struct {
	pathTargetMap       map[string]string
	pathReverseProxyMap map[string]*httputil.ReverseProxy

	State *State
}

func NewManagementService(state *State) *Management {
	routesFilepath := filepath.Join(state.GetRuntimePath(), RoutesFile)

	// try to load routes from routes.json
	pathTargetMap, err := loadPathTargetMapFrom(routesFilepath)
	if err != nil {
		logger.Error("Failed to load routes", zap.Any("error", err), zap.Any("filepath", routesFilepath))
		pathTargetMap = make(map[string]string)
	}

	pathReverseProxyMap := make(map[string]*httputil.ReverseProxy)

	for path, target := range pathTargetMap {
		targetURL, err := url.Parse(target)
		if err != nil {
			logger.Error("Failed to parse target", zap.Any("error", err), zap.String("target", target))
			continue
		}
		proxy := httputil.NewSingleHostReverseProxy(targetURL)
		proxy.Transport = uploadTransport
		proxy.FlushInterval = -1 // Stream responses immediately
		pathReverseProxyMap[path] = proxy
	}

	return &Management{
		pathTargetMap:       pathTargetMap,
		pathReverseProxyMap: pathReverseProxyMap,
		State:               state,
	}
}

func (g *Management) CreateRoute(route *model.Route) error {
	url, err := url.Parse(route.Target)
	if err != nil {
		return err
	}

	g.pathTargetMap[route.Path] = route.Target
	proxy := httputil.NewSingleHostReverseProxy(url)
	proxy.Transport = uploadTransport
	proxy.FlushInterval = -1
	g.pathReverseProxyMap[route.Path] = proxy

	routesFilePath := filepath.Join(g.State.GetRuntimePath(), RoutesFile)

	err = savePathTargetMapTo(routesFilePath, g.pathTargetMap)
	if err != nil {
		return err
	}

	return nil
}

func (g *Management) GetRoutes() []*model.Route {
	routes := make([]*model.Route, 0)

	for path, target := range g.pathTargetMap {
		routes = append(routes, &model.Route{
			Path:   path,
			Target: target,
		})
	}

	return routes
}

func (g *Management) GetProxy(path string) *httputil.ReverseProxy {
	// sort paths by length in descending order
	// (without this step, a path like "/abcd" can potentially be matched with "/ab")
	paths := getSortedKeys(g.pathReverseProxyMap)

	for _, p := range paths {
		if strings.HasPrefix(path, p) {
			return g.pathReverseProxyMap[p]
		}
	}
	return nil
}

func (g *Management) GetGatewayPort() string {
	return g.State.GetGatewayPort()
}

func (g *Management) SetGatewayPort(port string) error {
	if err := g.State.SetGatewayPort(port); err != nil {
		return err
	}

	return nil
}

func (g *Management) GetSSLEnabled() bool {
	return g.State.GetSSLEnabled()
}

func (g *Management) GetSSLPort() string {
	return g.State.GetSSLPort()
}

func (g *Management) GetSSLDomain() string {
	return g.State.GetSSLDomain()
}

func (g *Management) GetSSLCertType() string {
	return g.State.GetSSLCertType()
}

func (g *Management) SetSSLConfig(enabled bool, port string, domain string, certType string) error {
	if err := g.State.SetSSLEnabled(enabled); err != nil {
		return err
	}
	if err := g.State.SetSSLPort(port); err != nil {
		return err
	}
	if err := g.State.SetSSLDomain(domain); err != nil {
		return err
	}
	if err := g.State.SetSSLCertType(certType); err != nil {
		return err
	}
	return nil
}

func getSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))

	for key := range m {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })

	return keys
}

func loadPathTargetMapFrom(routesFilepath string) (map[string]string, error) {
	content, err := os.ReadFile(routesFilepath)
	if err != nil {
		return nil, err
	}

	pathTargetMap := make(map[string]string)
	err = json.Unmarshal(content, &pathTargetMap)
	if err != nil {
		return nil, err
	}

	return pathTargetMap, nil
}

func savePathTargetMapTo(routesFilepath string, pathTargetMap map[string]string) error {
	content, err := json.Marshal(pathTargetMap)
	if err != nil {
		return err
	}

	return os.WriteFile(routesFilepath, content, 0o600)
}

func GetCertDates(certPath string) (time.Time, time.Time, error) {
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	block, _ := pem.Decode(certBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, time.Time{}, errors.New("failed to decode certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return cert.NotBefore, cert.NotAfter, nil
}

func GenerateSelfSignedCert(certPath, keyPath, domain string) error {
	certDir := filepath.Dir(certPath)
	caCertPath := filepath.Join(certDir, "ca.crt")
	caKeyPath := filepath.Join(certDir, "ca.key")

	var caCert *x509.Certificate
	var caKey interface{}

	// 1. Load or Generate Root CA
	if _, err := os.Stat(caCertPath); os.IsNotExist(err) {
		// Generate CA private key
		caPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		caKey = caPriv

		notBefore := time.Now()
		notAfter := notBefore.Add(365 * 24 * time.Hour * 10) // 10 years validity

		serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
		serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
		if err != nil {
			return err
		}

		caTemplate := x509.Certificate{
			SerialNumber: serialNumber,
			Subject: pkix.Name{
				CommonName:         "NimoOS-CA",
				Organization:       []string{"NimoTech"},
				OrganizationalUnit: []string{"NimoOS"},
			},
			NotBefore:             notBefore,
			NotAfter:              notAfter,
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
			BasicConstraintsValid: true,
			IsCA:                  true,
		}

		caDerBytes, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caPriv.PublicKey, caPriv)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(certDir, 0755); err != nil {
			return err
		}

		caCertOut, err := os.Create(caCertPath)
		if err != nil {
			return err
		}
		defer caCertOut.Close()
		if err := pem.Encode(caCertOut, &pem.Block{Type: "CERTIFICATE", Bytes: caDerBytes}); err != nil {
			return err
		}

		caKeyOut, err := os.OpenFile(caKeyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		defer caKeyOut.Close()
		caPrivBytes, err := x509.MarshalECPrivateKey(caPriv)
		if err != nil {
			return err
		}
		if err := pem.Encode(caKeyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: caPrivBytes}); err != nil {
			return err
		}

		caCert, err = x509.ParseCertificate(caDerBytes)
		if err != nil {
			return err
		}
	} else {
		// Load existing CA cert and key
		caCertBytes, err := os.ReadFile(caCertPath)
		if err != nil {
			return err
		}
		caBlock, _ := pem.Decode(caCertBytes)
		if caBlock == nil {
			return errors.New("failed to decode CA cert PEM")
		}
		caCert, err = x509.ParseCertificate(caBlock.Bytes)
		if err != nil {
			return err
		}

		caKeyBytes, err := os.ReadFile(caKeyPath)
		if err != nil {
			return err
		}
		caKeyBlock, _ := pem.Decode(caKeyBytes)
		if caKeyBlock == nil {
			return errors.New("failed to decode CA key PEM")
		}
		caKey, err = x509.ParseECPrivateKey(caKeyBlock.Bytes)
		if err != nil {
			return err
		}
	}

	// 2. Generate Server Cert signed by CA
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour * 10) // 10 years validity

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:         "NimoOS Local",
			Organization:       []string{"NimoTech"},
			OrganizationalUnit: []string{"NimoOS"},
		},
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	if ip := net.ParseIP(domain); ip != nil {
		template.IPAddresses = append(template.IPAddresses, ip)
	} else {
		template.DNSNames = append(template.DNSNames, domain)
	}
	template.DNSNames = append(template.DNSNames, "localhost")
	template.IPAddresses = append(template.IPAddresses, net.ParseIP("127.0.0.1"), net.ParseIP("::1"))

	// Get local interface IPs and add them to IP SANs
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, i := range ifaces {
			addrs, err := i.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip != nil && !ip.IsLoopback() {
					template.IPAddresses = append(template.IPAddresses, ip)
				}
			}
		}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return err
	}

	certOut, err := os.Create(certPath)
	if err != nil {
		return err
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return err
	}

	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer keyOut.Close()
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes}); err != nil {
		return err
	}

	return nil
}
