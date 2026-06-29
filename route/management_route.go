package route

import (
	"crypto/ecdsa"
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/NimoTech/NimoOS-Common/external"
	"github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-Common/utils/common_err"
	"github.com/NimoTech/NimoOS-Common/utils/constants"
	"github.com/NimoTech/NimoOS-Common/utils/jwt"
	"github.com/NimoTech/NimoOS-Gateway/service"
	"github.com/labstack/echo/v4"
	echo_middleware "github.com/labstack/echo/v4/middleware"
)

type ManagementRoute struct {
	management *service.Management
}

func NewManagementRoute(management *service.Management) *ManagementRoute {
	return &ManagementRoute{
		management: management,
	}
}

func (m *ManagementRoute) GetRoute() http.Handler {
	e := echo.New()

	e.Use((echo_middleware.CORSWithConfig(echo_middleware.CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{echo.POST, echo.GET, echo.OPTIONS, echo.PUT, echo.DELETE},
		AllowHeaders:     []string{echo.HeaderAuthorization, echo.HeaderContentLength, echo.HeaderXCSRFToken, echo.HeaderContentType, echo.HeaderAccessControlAllowOrigin, echo.HeaderAccessControlAllowHeaders, echo.HeaderAccessControlAllowMethods, echo.HeaderConnection, echo.HeaderOrigin, echo.HeaderXRequestedWith},
		ExposeHeaders:    []string{echo.HeaderContentLength, echo.HeaderAccessControlAllowOrigin, echo.HeaderAccessControlAllowHeaders},
		MaxAge:           172800,
		AllowCredentials: true,
	})))

	e.Use(echo_middleware.Gzip())

	e.GET("/ping", func(ctx echo.Context) error {
		return ctx.JSON(http.StatusOK, echo.Map{
			"message": "pong from management service",
		})
	})

	m.buildV1Group(e)

	return e
}

func (m *ManagementRoute) buildV1Group(e *echo.Echo) {
	v1Group := e.Group("/v1")

	v1Group.Use()
	{
		m.buildV1RouteGroup(v1Group)
	}
}

func (m *ManagementRoute) buildV1RouteGroup(v1Group *echo.Group) {
	v1GatewayGroup := v1Group.Group("/gateway")

	v1GatewayGroup.Use()
	{
		v1GatewayGroup.GET("/routes", func(ctx echo.Context) error {
			return ctx.JSON(http.StatusOK, m.management.GetRoutes())
		})

		v1GatewayGroup.POST("/routes",
			func(ctx echo.Context) error {
				var route *model.Route
				err := ctx.Bind(&route)
				if err != nil {
					return ctx.JSON(http.StatusBadRequest, model.Result{
						Success: common_err.CLIENT_ERROR,
						Message: err.Error(),
					})
				}

				if err := m.management.CreateRoute(route); err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}

				return ctx.NoContent(http.StatusCreated)
			},
			echo_middleware.JWTWithConfig(echo_middleware.JWTConfig{
				Skipper: func(c echo.Context) bool {
					return c.RealIP() == "::1" || c.RealIP() == "127.0.0.1"
					// return true
				},
				ParseTokenFunc: func(token string, c echo.Context) (interface{}, error) {
					valid, claims, err := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(m.management.State.GetRuntimePath()) })
					if err != nil || !valid {
						return nil, echo.ErrUnauthorized
					}
					c.Request().Header.Set("user_id", strconv.Itoa(claims.ID))

					return claims, nil
				},
				TokenLookupFuncs: []echo_middleware.ValuesExtractor{
					func(c echo.Context) ([]string, error) {
						if len(c.Request().Header.Get(echo.HeaderAuthorization)) > 0 {
							return []string{c.Request().Header.Get(echo.HeaderAuthorization)}, nil
						}
						return []string{c.QueryParam("token")}, nil
					},
				},
			}))

		v1GatewayGroup.GET("/port", func(ctx echo.Context) error {
			return ctx.JSON(http.StatusOK, model.Result{
				Success: common_err.SUCCESS,
				Message: common_err.GetMsg(common_err.SUCCESS),
				Data:    m.management.GetGatewayPort(),
			})
		})

		v1GatewayGroup.PUT("/port",
			func(ctx echo.Context) error {
				var request *model.ChangePortRequest

				if err := ctx.Bind(&request); err != nil {
					return ctx.JSON(http.StatusBadRequest, model.Result{
						Success: common_err.CLIENT_ERROR,
						Message: err.Error(),
					})
				}

				if err := m.management.SetGatewayPort(request.Port); err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}

				return ctx.JSON(http.StatusOK, model.Result{
					Success: common_err.SUCCESS,
					Message: common_err.GetMsg(common_err.SUCCESS),
				})
			},
			echo_middleware.JWTWithConfig(echo_middleware.JWTConfig{
				Skipper: func(c echo.Context) bool {
					return c.RealIP() == "::1" || c.RealIP() == "127.0.0.1"
					// return true
				},
				ParseTokenFunc: func(token string, c echo.Context) (interface{}, error) {
					valid, claims, err := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(m.management.State.GetRuntimePath()) })
					if err != nil || !valid {
						return nil, echo.ErrUnauthorized
					}
					c.Request().Header.Set("user_id", strconv.Itoa(claims.ID))

					return claims, nil
				},
				TokenLookupFuncs: []echo_middleware.ValuesExtractor{
					func(c echo.Context) ([]string, error) {
						if len(c.Request().Header.Get(echo.HeaderAuthorization)) > 0 {
							return []string{c.Request().Header.Get(echo.HeaderAuthorization)}, nil
						}
						return []string{c.QueryParam("token")}, nil
					},
				},
			}))

		v1GatewayGroup.GET("/ssl", func(ctx echo.Context) error {
			enabled := m.management.GetSSLEnabled()
			port := m.management.GetSSLPort()
			domain := m.management.GetSSLDomain()
			certType := m.management.GetSSLCertType()

			certPath := filepath.Join(constants.DefaultConfigPath, "certs", "gateway.crt")
			var notBefore, notAfter time.Time
			if _, err := os.Stat(certPath); err == nil {
				if nb, na, err := service.GetCertDates(certPath); err == nil {
					notBefore = nb
					notAfter = na
				}
			}

			return ctx.JSON(http.StatusOK, model.Result{
				Success: common_err.SUCCESS,
				Message: common_err.GetMsg(common_err.SUCCESS),
				Data: model.SSLConfigResponse{
					Enabled:        enabled,
					Port:           port,
					Domain:         domain,
					CertType:       certType,
					EffectiveTime:  notBefore,
					ExpirationTime: notAfter,
				},
			})
		})

		v1GatewayGroup.GET("/ssl/ca", func(ctx echo.Context) error {
			caPath := filepath.Join(constants.DefaultConfigPath, "certs", "ca.crt")
			if _, err := os.Stat(caPath); os.IsNotExist(err) {
				return ctx.JSON(http.StatusNotFound, model.Result{
					Success: common_err.CLIENT_ERROR,
					Message: "CA certificate not found",
				})
			}
			return ctx.Attachment(caPath, "nimoos-ca.crt")
		})

		v1GatewayGroup.PUT("/ssl",
			func(ctx echo.Context) error {
				var request model.SSLConfigRequest
				if err := ctx.Bind(&request); err != nil {
					return ctx.JSON(http.StatusBadRequest, model.Result{
						Success: common_err.CLIENT_ERROR,
						Message: err.Error(),
					})
				}

				if err := m.management.SetSSLConfig(request.Enabled, request.Port, request.Domain, request.CertType); err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}

				return ctx.JSON(http.StatusOK, model.Result{
					Success: common_err.SUCCESS,
					Message: common_err.GetMsg(common_err.SUCCESS),
				})
			},
			echo_middleware.JWTWithConfig(echo_middleware.JWTConfig{
				Skipper: func(c echo.Context) bool {
					return c.RealIP() == "::1" || c.RealIP() == "127.0.0.1"
				},
				ParseTokenFunc: func(token string, c echo.Context) (interface{}, error) {
					valid, claims, err := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(m.management.State.GetRuntimePath()) })
					if err != nil || !valid {
						return nil, echo.ErrUnauthorized
					}
					c.Request().Header.Set("user_id", strconv.Itoa(claims.ID))

					return claims, nil
				},
				TokenLookupFuncs: []echo_middleware.ValuesExtractor{
					func(c echo.Context) ([]string, error) {
						if len(c.Request().Header.Get(echo.HeaderAuthorization)) > 0 {
							return []string{c.Request().Header.Get(echo.HeaderAuthorization)}, nil
						}
						return []string{c.QueryParam("token")}, nil
					},
				},
			}))

		v1GatewayGroup.POST("/ssl/upload",
			func(ctx echo.Context) error {
				certFile, err := ctx.FormFile("crt")
				if err != nil {
					return ctx.JSON(http.StatusBadRequest, model.Result{
						Success: common_err.CLIENT_ERROR,
						Message: "missing 'crt' file: " + err.Error(),
					})
				}

				keyFile, err := ctx.FormFile("pem")
				if err != nil {
					keyFile, err = ctx.FormFile("key")
					if err != nil {
						return ctx.JSON(http.StatusBadRequest, model.Result{
							Success: common_err.CLIENT_ERROR,
							Message: "missing 'pem' or 'key' file: " + err.Error(),
						})
					}
				}

				srcCert, err := certFile.Open()
				if err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}
				defer srcCert.Close()

				srcKey, err := keyFile.Open()
				if err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}
				defer srcKey.Close()

				certsDir := filepath.Join(constants.DefaultConfigPath, "certs")
				if err := os.MkdirAll(certsDir, 0755); err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}

				dstCertPath := filepath.Join(certsDir, "gateway.crt")
				dstCert, err := os.Create(dstCertPath)
				if err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}
				defer dstCert.Close()
				if _, err := io.Copy(dstCert, srcCert); err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}

				dstKeyPath := filepath.Join(certsDir, "gateway.key")
				dstKey, err := os.OpenFile(dstKeyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				if err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}
				defer dstKey.Close()
				if _, err := io.Copy(dstKey, srcKey); err != nil {
					return ctx.JSON(http.StatusInternalServerError, model.Result{
						Success: common_err.SERVICE_ERROR,
						Message: err.Error(),
					})
				}

				_, err = tls.LoadX509KeyPair(dstCertPath, dstKeyPath)
				if err != nil {
					os.Remove(dstCertPath)
					os.Remove(dstKeyPath)
					return ctx.JSON(http.StatusBadRequest, model.Result{
						Success: common_err.CLIENT_ERROR,
						Message: "invalid certificate and key pair: " + err.Error(),
					})
				}

				if m.management.GetSSLEnabled() {
					if err := m.management.SetSSLConfig(true, m.management.GetSSLPort(), m.management.GetSSLDomain(), "custom"); err != nil {
						return ctx.JSON(http.StatusInternalServerError, model.Result{
							Success: common_err.SERVICE_ERROR,
							Message: err.Error(),
						})
					}
				}

				return ctx.JSON(http.StatusOK, model.Result{
					Success: common_err.SUCCESS,
					Message: common_err.GetMsg(common_err.SUCCESS),
				})
			},
			echo_middleware.JWTWithConfig(echo_middleware.JWTConfig{
				Skipper: func(c echo.Context) bool {
					return c.RealIP() == "::1" || c.RealIP() == "127.0.0.1"
				},
				ParseTokenFunc: func(token string, c echo.Context) (interface{}, error) {
					valid, claims, err := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(m.management.State.GetRuntimePath()) })
					if err != nil || !valid {
						return nil, echo.ErrUnauthorized
					}
					c.Request().Header.Set("user_id", strconv.Itoa(claims.ID))

					return claims, nil
				},
				TokenLookupFuncs: []echo_middleware.ValuesExtractor{
					func(c echo.Context) ([]string, error) {
						if len(c.Request().Header.Get(echo.HeaderAuthorization)) > 0 {
							return []string{c.Request().Header.Get(echo.HeaderAuthorization)}, nil
						}
						return []string{c.QueryParam("token")}, nil
					},
				},
			}))
	}
}
