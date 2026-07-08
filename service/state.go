package service

type State struct {
	gatewayPort         string
	onGatewayPortChange []func(string) error

	sslEnabled            bool
	sslPort               string
	sslDomain             string
	sslCertType           string
	onGatewayConfigChange []func() error

	runtimePath string
	wwwPath     string

	qdrantURL    string
	ollamaURL    string
	dockerSocket string
}

func NewState() *State {
	return &State{
		gatewayPort:           "",
		onGatewayPortChange:   make([]func(string) error, 0),
		sslEnabled:            false,
		sslPort:               "",
		sslDomain:             "",
		sslCertType:           "",
		onGatewayConfigChange: make([]func() error, 0),

		runtimePath: "",
		wwwPath:     "",

		qdrantURL:    "",
		ollamaURL:    "",
		dockerSocket: "",
	}
}

func (c *State) SetGatewayPort(port string) (err error) {
	defer func() {
		if err == nil {
			c.gatewayPort = port
		}
	}()
	if err := c.notifyOnGatewayPortChange(port); err != nil {
		return err
	}
	return c.notifyOnGatewayConfigChange()
}

func (c *State) GetGatewayPort() string {
	return c.gatewayPort
}

// Add func `f` to the stack. The stack of funcs will be called, in reverse order, when there is request to change the port.
func (c *State) OnGatewayPortChange(f func(string) error) {
	c.onGatewayPortChange = append(c.onGatewayPortChange, f)
}

func (c *State) notifyOnGatewayPortChange(port string) error {
	for i := len(c.onGatewayPortChange) - 1; i >= 0; i-- {
		if err := c.onGatewayPortChange[i](port); err != nil {
			return err
		}
	}

	return nil
}

func (c *State) OnGatewayConfigChange(f func() error) {
	c.onGatewayConfigChange = append(c.onGatewayConfigChange, f)
}

func (c *State) notifyOnGatewayConfigChange() error {
	for i := len(c.onGatewayConfigChange) - 1; i >= 0; i-- {
		if err := c.onGatewayConfigChange[i](); err != nil {
			return err
		}
	}
	return nil
}

func (c *State) SetSSLEnabled(enabled bool) error {
	c.sslEnabled = enabled
	return c.notifyOnGatewayConfigChange()
}

func (c *State) GetSSLEnabled() bool {
	return c.sslEnabled
}

func (c *State) SetSSLPort(port string) error {
	c.sslPort = port
	return c.notifyOnGatewayConfigChange()
}

func (c *State) GetSSLPort() string {
	return c.sslPort
}

func (c *State) SetSSLDomain(domain string) error {
	c.sslDomain = domain
	return c.notifyOnGatewayConfigChange()
}

func (c *State) GetSSLDomain() string {
	return c.sslDomain
}

func (c *State) SetSSLCertType(certType string) error {
	c.sslCertType = certType
	return c.notifyOnGatewayConfigChange()
}

func (c *State) GetSSLCertType() string {
	return c.sslCertType
}

func (c *State) SetRuntimePath(path string) error {
	c.runtimePath = path
	return nil
}

func (c *State) GetRuntimePath() string {
	return c.runtimePath
}

func (c *State) SetWWWPath(path string) error {
	c.wwwPath = path
	return nil
}

func (c *State) GetWWWPath() string {
	return c.wwwPath
}

func (s *State) GetQdrantURL() string     { return s.qdrantURL }
func (s *State) SetQdrantURL(v string)    { s.qdrantURL = v }
func (s *State) GetOllamaURL() string     { return s.ollamaURL }
func (s *State) SetOllamaURL(v string)    { s.ollamaURL = v }
func (s *State) GetDockerSocket() string  { return s.dockerSocket }
func (s *State) SetDockerSocket(v string) { s.dockerSocket = v }
