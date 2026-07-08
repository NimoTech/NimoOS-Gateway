package common

import (
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/viper"

	"github.com/NimoTech/NimoOS-Common/utils/constants"
)

const (
	ConfigKeyLogPath     = "gateway.LogPath"
	ConfigKeyLogSaveName = "gateway.LogSaveName"
	ConfigKeyLogFileExt  = "gateway.LogFileExt"
	ConfigKeyGatewayPort = "gateway.Port"
	ConfigKeyRuntimePath = "common.RuntimePath"

	ConfigKeySSLEnabled  = "gateway.SSLEnabled"
	ConfigKeySSLPort     = "gateway.SSLPort"
	ConfigKeySSLDomain   = "gateway.SSLDomain"
	ConfigKeySSLCertType = "gateway.SSLCertType"

	ConfigKeyQdrantURL    = "components.QdrantURL"
	ConfigKeyOllamaURL    = "components.OllamaURL"
	ConfigKeyDockerSocket = "components.DockerSocket"
	ConfigKeyPhotosMLURL  = "components.PhotosMLURL"

	GatewayName       = "gateway"
	GatewayConfigType = "ini"
)

func LoadConfig() (*viper.Viper, error) {
	config := viper.New()

	config.SetDefault(ConfigKeyLogPath, constants.DefaultLogPath)
	config.SetDefault(ConfigKeyLogSaveName, GatewayName)
	config.SetDefault(ConfigKeyLogFileExt, "log")

	config.SetDefault(ConfigKeyRuntimePath, constants.DefaultRuntimePath) // See https://refspecs.linuxfoundation.org/FHS_3.0/fhs/ch05s13.html
	config.SetDefault(ConfigKeySSLEnabled, false)
	config.SetDefault(ConfigKeySSLPort, "443")
	config.SetDefault(ConfigKeySSLDomain, "nimoos.local")
	config.SetDefault(ConfigKeySSLCertType, "auto")

	config.SetDefault(ConfigKeyQdrantURL, "http://127.0.0.1:6333")
	config.SetDefault(ConfigKeyOllamaURL, "http://127.0.0.1:11434")
	config.SetDefault(ConfigKeyDockerSocket, "/var/run/docker.sock")
	config.SetDefault(ConfigKeyPhotosMLURL, "http://127.0.0.1:3003")

	config.SetConfigName(GatewayName)
	config.SetConfigType(GatewayConfigType)

	if currentDirectory, err := os.Getwd(); err != nil {
		log.Println(err)
	} else {
		config.AddConfigPath(currentDirectory)
		config.AddConfigPath(filepath.Join(currentDirectory, "conf"))
	}

	if configPath, success := os.LookupEnv("NIMOOS_CONFIG_PATH"); success {
		config.AddConfigPath(configPath)
	}

	config.AddConfigPath(constants.DefaultConfigPath)

	if err := config.ReadInConfig(); err != nil {
		return nil, err
	}

	return config, nil
}
