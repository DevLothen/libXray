package xray

import (
	"bytes"
	"os"
	"runtime/debug"
	"strconv"
	"sync"

	"github.com/xtls/libxray/memory"
	"github.com/xtls/xray-core/common/cmdarg"
	"github.com/xtls/xray-core/common/platform"
	"github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
)

var (
	coreServer    *core.Instance
	runtimeMu     sync.Mutex
	runtimeConfig *core.Config
)

func loadConfigFromPath(configPath string) (*core.Config, error) {
	file := cmdarg.Arg{configPath}
	return core.LoadConfig("json", file)
}

func loadConfigFromJSON(configJSON string) (*core.Config, error) {
	return core.LoadConfig("json", bytes.NewReader([]byte(configJSON)))
}

func startServer(config *core.Config) (*core.Instance, error) {
	server, err := core.New(config)
	if err != nil {
		return nil, err
	}

	return server, nil
}

func StartXray(configPath string) (*core.Instance, error) {
	config, err := loadConfigFromPath(configPath)
	if err != nil {
		return nil, err
	}

	return startServer(config)
}

func StartXrayFromJSON(configJSON string) (*core.Instance, error) {
	config, err := loadConfigFromJSON(configJSON)
	if err != nil {
		return nil, err
	}

	server, err := startServer(config)
	if err != nil {
		return nil, err
	}

	if err := server.Start(); err != nil {
		return nil, err
	}

	runtimeConfig = config
	return server, nil
}

// SetTunFd sets the TUN file descriptor.
// Call this BEFORE RunXray/RunXrayFromJSON.
func SetTunFd(fd int32) {
	os.Setenv(platform.TunFdKey, strconv.Itoa(int(fd)))
}

func InitEnv(datDir string) {
	os.Setenv(platform.AssetLocation, datDir)
	os.Setenv(platform.CertLocation, datDir)
}

// Run Xray instance.
// datDir means the dir which geosite.dat and geoip.dat are in.
// configPath means the config.json file path.
func RunXray(datDir, configPath string) (err error) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	InitEnv(datDir)
	memory.InitForceFree()
	config, err := loadConfigFromPath(configPath)
	if err != nil {
		return
	}

	coreServer, err = startServer(config)
	if err != nil {
		return
	}

	if err = coreServer.Start(); err != nil {
		return
	}

	runtimeConfig = config
	debug.FreeOSMemory()
	return nil
}

// Run Xray instance with JSON configuration string.
// datDir means the dir which geosite.dat and geoip.dat are in.
// configJSON means the JSON configuration string.
func RunXrayFromJSON(datDir, configJSON string) (err error) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	InitEnv(datDir)
	memory.InitForceFree()
	coreServer, err = StartXrayFromJSON(configJSON)
	if err != nil {
		return
	}

	debug.FreeOSMemory()
	return nil
}

// Get Xray State
func GetXrayState() bool {
	return coreServer != nil && coreServer.IsRunning()
}

// Stop Xray instance.
func StopXray() error {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	if coreServer != nil {
		err := coreServer.Close()
		coreServer = nil
		runtimeConfig = nil
		if err != nil {
			return err
		}
	}
	return nil
}

// Xray's version
func XrayVersion() string {
	return core.Version()
}
