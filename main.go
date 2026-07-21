package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/NimoTech/NimoOS-Common/external"
	"github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-Common/utils/constants"
	http2 "github.com/NimoTech/NimoOS-Common/utils/http"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/coreos/go-systemd/daemon"

	"github.com/NimoTech/NimoOS-Gateway/common"
	"github.com/NimoTech/NimoOS-Gateway/route"
	"github.com/NimoTech/NimoOS-Gateway/service"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

const localhost = "127.0.0.1"

var (
	commit = "private build"
	date   = "private build"

	_state   *service.State
	_gateway *http.Server

	_managementServiceReady = make(chan struct{})
	_gatewayServiceReady    = make(chan struct{})

	ErrCheckURLNotOK = errors.New("check url did not return 200 OK")

	//go:embed build/sysroot/etc/nimoos/gateway.ini.sample
	_confSample string
)

func init() {
	versionFlag := flag.Bool("v", false, "version")
	wwwPathFlag := flag.String("w", filepath.Join(constants.DefaultDataPath, "www"), "www path")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("v%s\n", common.Version)
		os.Exit(0)
	}

	println("git commit:", commit)
	println("build date:", date)

	_state = service.NewState()

	// create default config file if not exist
	ConfigFilePath := filepath.Join(constants.DefaultConfigPath, common.GatewayName+"."+common.GatewayConfigType)
	if _, err := os.Stat(ConfigFilePath); os.IsNotExist(err) {
		fmt.Println("config file not exist, create it")
		// create config file
		file, err := os.Create(ConfigFilePath)
		if err != nil {
			panic(err)
		}
		defer file.Close()

		// write default config
		_, err = file.WriteString(_confSample)
		if err != nil {
			panic(err)
		}
	}

	config, err := common.LoadConfig()
	if err != nil {
		panic(err)
	}

	logger.LogInit(
		config.GetString(common.ConfigKeyLogPath),
		config.GetString(common.ConfigKeyLogSaveName),
		config.GetString(common.ConfigKeyLogFileExt),
	)

	runtimePath := config.GetString(common.ConfigKeyRuntimePath)
	if err := _state.SetRuntimePath(runtimePath); err != nil {
		logger.Error("Failed to set runtime path", zap.Any("error", err), zap.Any(common.ConfigKeyRuntimePath, runtimePath))
		panic(err)
	}

	gatewayPort := config.GetString(common.ConfigKeyGatewayPort)
	if err := _state.SetGatewayPort(gatewayPort); err != nil {
		logger.Error("Failed to set gateway port", zap.Any("error", err), zap.Any(common.ConfigKeyGatewayPort, gatewayPort))
		panic(err)
	}

	sslEnabled := config.GetBool(common.ConfigKeySSLEnabled)
	sslPort := config.GetString(common.ConfigKeySSLPort)
	sslDomain := config.GetString(common.ConfigKeySSLDomain)
	sslCertType := config.GetString(common.ConfigKeySSLCertType)

	_state.SetSSLEnabled(sslEnabled)
	_state.SetSSLPort(sslPort)
	_state.SetSSLDomain(sslDomain)
	_state.SetSSLCertType(sslCertType)

	if err := _state.SetWWWPath(*wwwPathFlag); err != nil {
		logger.Error("Failed to set www path", zap.Any("error", err), zap.String("wwwpath", *wwwPathFlag))
		panic(err)
	}

	_state.SetQdrantURL(config.GetString(common.ConfigKeyQdrantURL))
	_state.SetOllamaURL(config.GetString(common.ConfigKeyOllamaURL))
	_state.SetDockerSocket(config.GetString(common.ConfigKeyDockerSocket))
	_state.SetPhotosMLURL(config.GetString(common.ConfigKeyPhotosMLURL))

	if err := checkPrequisites(_state); err != nil {
		logger.Error("Failed to check prequisites", zap.Any("error", err))
		panic(err)
	}

	_state.OnGatewayPortChange(func(port string) error {
		config.Set(common.ConfigKeyGatewayPort, port)
		return config.WriteConfig()
	})

	_state.OnGatewayConfigChange(func() error {
		config.Set(common.ConfigKeyGatewayPort, _state.GetGatewayPort())
		config.Set(common.ConfigKeySSLEnabled, _state.GetSSLEnabled())
		config.Set(common.ConfigKeySSLPort, _state.GetSSLPort())
		config.Set(common.ConfigKeySSLDomain, _state.GetSSLDomain())
		config.Set(common.ConfigKeySSLCertType, _state.GetSSLCertType())
		return config.WriteConfig()
	})
}

func main() {
	pidFilename, err := writePidFile(_state.GetRuntimePath())
	if err != nil {
		logger.Error("Failed to write pid file to runtime path", zap.Any("error", err), zap.Any("runtimePath", _state.GetRuntimePath()))
		panic(err)
	}

	defer cleanupFiles(
		_state.GetRuntimePath(),
		pidFilename, external.ManagementURLFilename, external.StaticURLFilename,
	)

	defer func() {
		if _gateway != nil {
			if err := _gateway.Shutdown(context.Background()); err != nil {
				logger.Error("Failed to stop gateway", zap.Any("error", err))
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	kill := make(chan os.Signal, 1)
	signal.Notify(kill, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-kill
		cancel()
	}()

	go func() {
		<-_managementServiceReady
		<-_gatewayServiceReady

		if supported, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
			logger.Error("Failed to notify systemd that gateway is ready", zap.Any("error", err))
		} else if supported {
			logger.Info("Notified systemd that gateway is ready")
		} else {
			logger.Info("This process is not running as a systemd service.")
		}
	}()

	app := fx.New(
		fx.Provide(func() *service.State { return _state }),
		fx.Provide(service.NewManagementService),
		fx.Provide(route.NewManagementRoute),
		fx.Provide(route.NewGatewayRoute),
		fx.Provide(route.NewStaticRoute),
		fx.Invoke(run),
	)

	if err := app.Start(ctx); err != nil {
		if err != context.Canceled {
			logger.Error("Failed to start gateway", zap.Any("error", err))
		}
	}
}

func run(
	lifecycle fx.Lifecycle,
	management *service.Management,
	managementRoute *route.ManagementRoute,
	gatewayRoute *route.GatewayRoute,
	staticRoute *route.StaticRoute,
) {
	// management server
	lifecycle.Append(
		fx.Hook{
			OnStart: func(context.Context) error {
				listener, err := net.Listen("tcp", net.JoinHostPort(localhost, "0"))
				if err != nil {
					return err
				}

				managementServer := &http.Server{
					Handler:           managementRoute.GetRoute(),
					ReadHeaderTimeout: 5 * time.Second,
				}

				urlFilePath, err := writeAddressFile(_state.GetRuntimePath(), external.ManagementURLFilename, "http://"+listener.Addr().String())
				if err != nil {
					return err
				}

				go func() {
					logger.Info("Management service is listening...",
						zap.Any("address", listener.Addr().String()),
						zap.Any("filepath", urlFilePath),
					)
					err := managementServer.Serve(listener)
					if err != nil {
						logger.Error("management server error", zap.Any("error", err))
						os.Exit(1)
					}
				}()

				if err := management.CreateRoute(&model.Route{
					Path:   "/v1/gateway/port",
					Target: "http://" + listener.Addr().String(),
				}); err != nil {
					return err
				}

				if err := management.CreateRoute(&model.Route{
					Path:   "/v1/gateway/components",
					Target: "http://" + listener.Addr().String(),
				}); err != nil {
					return err
				}

				if err := management.CreateRoute(&model.Route{
					Path:   "/v1/gateway/device-info",
					Target: "http://" + listener.Addr().String(),
				}); err != nil {
					return err
				}

				if err := management.CreateRoute(&model.Route{
					Path:   "/v1/gateway/lan-discovery",
					Target: "http://" + listener.Addr().String(),
				}); err != nil {
					return err
				}

				if err := management.CreateRoute(&model.Route{
					Path:   "/v1/gateway/ssl",
					Target: "http://" + listener.Addr().String(),
				}); err != nil {
					return err
				}

				if err := management.CreateRoute(&model.Route{
					Path:   "/v1/gateway/ssl/upload",
					Target: "http://" + listener.Addr().String(),
				}); err != nil {
					return err
				}

				if err := management.CreateRoute(&model.Route{
					Path:   "/v1/gateway/ssl/ca",
					Target: "http://" + listener.Addr().String(),
				}); err != nil {
					return err
				}

				_managementServiceReady <- struct{}{}

				return nil
			},
		},
	)

	// gateway service
	lifecycle.Append(
		fx.Hook{
			OnStart: func(ctx context.Context) error {
				route := gatewayRoute.GetRoute()

				if _state.GetGatewayPort() == "" {
					// check if a port is available starting from port 80/8080
					portsToCheck := []int{}
					for i := 80; i < 90; i++ {
						portsToCheck = append(portsToCheck, i)
					}

					for i := 8080; i < 8090; i++ {
						portsToCheck = append(portsToCheck, i)
					}

					port := ""
					for _, p := range portsToCheck {
						port = fmt.Sprintf("%d", p)
						logger.Info("Checking if port is available...", zap.Any("port", port))
						if listener, err := net.Listen("tcp", net.JoinHostPort("", port)); err == nil {
							if err = listener.Close(); err != nil {
								logger.Error("Failed to close listener", zap.Any("error", err), zap.Any("port", port))
								continue
							}
							break
						}
					}

					if port == "" {
						return errors.New("No port available for gateway to use")
					}

					if err := _state.SetGatewayPort(port); err != nil {
						return err
					}
				}

				_state.OnGatewayConfigChange(func() error {
					return reloadGateways(_state, route)
				})

				if err := reloadGateways(_state, route); err != nil {
					return err
				}

				_gatewayServiceReady <- struct{}{}

				return nil
			},
		})

	// static web
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			listener, err := net.Listen("tcp", net.JoinHostPort(localhost, "0"))
			if err != nil {
				return err
			}

			staticServer := &http.Server{
				Handler:           staticRoute.GetRoute(),
				ReadHeaderTimeout: 5 * time.Second,
			}

			target := "http://" + listener.Addr().String()

			urlFilePath, err := writeAddressFile(_state.GetRuntimePath(), external.StaticURLFilename, target)
			if err != nil {
				return err
			}

			if err := management.CreateRoute(&model.Route{
				Path:   "/",
				Target: target,
			}); err != nil {
				return err
			}

			logger.Info(
				"Static web service is listening...",
				zap.Any("address", listener.Addr().String()),
				zap.Any("filepath", urlFilePath),
			)
			return staticServer.Serve(listener)
		},
	})
}

var _sslGateway *http.Server

func reloadGateways(state *service.State, route *http.ServeMux) error {
	httpPort := state.GetGatewayPort()
	if httpPort != "" {
		if err := reloadHTTPGateway(httpPort, route); err != nil {
			return err
		}
	} else {
		if _gateway != nil {
			gatewayOld := _gateway
			_gateway = nil
			go func() {
				logger.Info("Stopping HTTP gateway...", zap.Any("address", gatewayOld.Addr))
				if err := gatewayOld.Shutdown(context.Background()); err != nil {
					logger.Error("Error when stopping HTTP gateway", zap.Any("error", err), zap.Any("address", gatewayOld.Addr))
				}
			}()
		}
	}

	sslEnabled := state.GetSSLEnabled()
	sslPort := state.GetSSLPort()

	if sslEnabled && sslPort != "" {
		certDir := filepath.Join(constants.DefaultConfigPath, "certs")
		certFile := filepath.Join(certDir, "gateway.crt")
		keyFile := filepath.Join(certDir, "gateway.key")

		if state.GetSSLCertType() == "auto" {
			needsGeneration := false
			caCertFile := filepath.Join(certDir, "ca.crt")
			if _, err := os.Stat(certFile); os.IsNotExist(err) {
				needsGeneration = true
			} else if _, err := os.Stat(caCertFile); os.IsNotExist(err) {
				needsGeneration = true
			} else {
				if nb, _, err := service.GetCertDates(certFile); err != nil || time.Now().After(nb.Add(365*24*time.Hour*10)) {
					needsGeneration = true
				} else {
					certBytes, err := os.ReadFile(certFile)
					if err == nil {
						block, _ := pem.Decode(certBytes)
						if block != nil {
							cert, err := x509.ParseCertificate(block.Bytes)
							if err == nil && cert.Subject.CommonName != state.GetSSLDomain() {
								needsGeneration = true
							}
						}
					}
				}
			}

			if needsGeneration {
				logger.Info("Generating self-signed certificate...", zap.String("domain", state.GetSSLDomain()))
				if err := service.GenerateSelfSignedCert(certFile, keyFile, state.GetSSLDomain()); err != nil {
					logger.Error("Failed to generate self-signed certificate", zap.Error(err))
					return err
				}
			}
		}

		if err := reloadHTTPSGateway(sslPort, route, certFile, keyFile); err != nil {
			return err
		}
	} else {
		if _sslGateway != nil {
			sslGatewayOld := _sslGateway
			_sslGateway = nil
			go func() {
				logger.Info("Stopping SSL gateway...", zap.Any("address", sslGatewayOld.Addr))
				if err := sslGatewayOld.Shutdown(context.Background()); err != nil {
					logger.Error("Error when stopping SSL gateway", zap.Any("error", err), zap.Any("address", sslGatewayOld.Addr))
				}
			}()
		}
	}

	return nil
}

func reloadHTTPGateway(port string, route *http.ServeMux) error {
	if _gateway != nil {
		_, runningPort, err := net.SplitHostPort(_gateway.Addr)
		if err == nil && runningPort == port {
			logger.Info("HTTP Port is the same as current running gateway - no change is required")
			return nil
		}
	}

	listener, err := net.Listen("tcp", net.JoinHostPort("", port))
	if err != nil {
		return err
	}

	addr := listener.Addr().String()

	gatewayNew := &http.Server{
		Addr:              addr,
		Handler:           route,
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		err := gatewayNew.Serve(listener)
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				logger.Info("HTTP gateway is stopped", zap.Any("address", gatewayNew.Addr))
				return
			}
			logger.Error("Error when serving HTTP gateway", zap.Any("error", err), zap.Any("address", gatewayNew.Addr))
		}
	}()

	url := "http://127.0.0.1:" + port + "/ping"
	if err := checkURLWithRetry(url, 10); err != nil {
		return err
	}

	logger.Info("New HTTP gateway is listening...", zap.Any("address", gatewayNew.Addr))

	if _gateway != nil {
		gatewayOld := _gateway
		go func() {
			logger.Info("Stopping previous HTTP gateway in 1 seconds...", zap.Any("address", gatewayOld.Addr))
			time.Sleep(time.Second)
			if err := gatewayOld.Shutdown(context.Background()); err != nil {
				logger.Error("Error when stopping previous HTTP gateway", zap.Any("error", err), zap.Any("address", gatewayOld.Addr))
			}
		}()
	}

	_gateway = gatewayNew
	return nil
}

func reloadHTTPSGateway(port string, route *http.ServeMux, certFile, keyFile string) error {
	if _sslGateway != nil {
		_, runningPort, err := net.SplitHostPort(_sslGateway.Addr)
		if err == nil && runningPort == port {
			logger.Info("HTTPS Port is the same as current running gateway - no change is required")
			return nil
		}
	}

	listener, err := net.Listen("tcp", net.JoinHostPort("", port))
	if err != nil {
		return err
	}

	addr := listener.Addr().String()

	sslGatewayNew := &http.Server{
		Addr:              addr,
		Handler:           route,
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		err := sslGatewayNew.ServeTLS(listener, certFile, keyFile)
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				logger.Info("HTTPS gateway is stopped", zap.Any("address", sslGatewayNew.Addr))
				return
			}
			logger.Error("Error when serving HTTPS gateway", zap.Any("error", err), zap.Any("address", sslGatewayNew.Addr))
		}
	}()

	url := "https://127.0.0.1:" + port + "/ping"
	if err := checkHTTPSURLWithRetry(url, 10); err != nil {
		return err
	}

	logger.Info("New HTTPS gateway is listening...", zap.Any("address", sslGatewayNew.Addr))

	if _sslGateway != nil {
		sslGatewayOld := _sslGateway
		go func() {
			logger.Info("Stopping previous HTTPS gateway in 1 seconds...", zap.Any("address", sslGatewayOld.Addr))
			time.Sleep(time.Second)
			if err := sslGatewayOld.Shutdown(context.Background()); err != nil {
				logger.Error("Error when stopping previous HTTPS gateway", zap.Any("error", err), zap.Any("address", sslGatewayOld.Addr))
			}
		}()
	}

	_sslGateway = sslGatewayNew
	return nil
}

func checkHTTPSURL(urlStr string) error {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	response, err := client.Get(urlStr)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrCheckURLNotOK
	}
	return nil
}

func checkHTTPSURLWithRetry(url string, retry uint) error {
	count := retry
	var err error

	for count >= 0 {
		logger.Info("Checking if SSL service at URL is running...", zap.Any("url", url), zap.Any("retry", count))
		if err = checkHTTPSURL(url); err != nil {
			time.Sleep(time.Second)
			count--
			continue
		}
		break
	}

	return err
}

func checkURLWithRetry(url string, retry uint) error {
	count := retry
	var err error

	for count >= 0 {
		logger.Info("Checking if service at URL is running...", zap.Any("url", url), zap.Any("retry", count))
		if err = checkURL(url); err != nil {
			time.Sleep(time.Second)
			count--
			continue
		}
		break
	}

	return err
}

func checkURL(url string) error {
	response, err := http2.Get(url, 5*time.Second)
	if err == nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusOK {
		return ErrCheckURLNotOK
	}

	return nil
}

func writePidFile(runtimePath string) (string, error) {
	filename := "gateway.pid"
	filepath := filepath.Join(runtimePath, filename)
	return filename, os.WriteFile(filepath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600)
}

func writeAddressFile(runtimePath string, filename string, address string) (string, error) {
	err := os.MkdirAll(runtimePath, 0o755)
	if err != nil {
		return "", err
	}

	filepath := filepath.Join(runtimePath, filename)
	return filepath, os.WriteFile(filepath, []byte(address), 0o600)
}

func cleanupFiles(runtimePath string, filenames ...string) {
	for _, filename := range filenames {
		err := os.Remove(filepath.Join(runtimePath, filename))
		if err != nil {
			logger.Error("Failed to cleanup file", zap.Any("error", err), zap.Any("filename", filename))
		}
	}
}

func checkPrequisites(state *service.State) error {
	path := state.GetRuntimePath()

	err := os.MkdirAll(path, 0o755)
	if err != nil {
		return fmt.Errorf("please ensure the owner of this service has write permission to that path %s", path)
	}

	return nil
}
