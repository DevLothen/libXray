package xray

import (
	"bytes"
	"errors"
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
	coreServerMu  sync.Mutex
	coreServer    *core.Instance
	runtimeConfig *core.Config
)

var ErrAlreadyRunning = errors.New("xray is already running")

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

	return startServer(config)
}

func SetTunFd(fd int32) {
	os.Setenv(platform.TunFdKey, strconv.Itoa(int(fd)))
}

func InitEnv(datDir string) {
	if datDir == "" {
		return
	}
	os.Setenv(platform.AssetLocation, datDir)
	os.Setenv(platform.CertLocation, datDir)
}

// Run Xray instance.
// configPath means the config.json file path.
func RunXray(configPath string) (err error) {
	coreServerMu.Lock()
	defer coreServerMu.Unlock()
	if coreServer != nil {
		return ErrAlreadyRunning
	}

	memory.InitForceFree()
	config, err := loadConfigFromPath(configPath)
	if err != nil {
		return
	}

	server, err := startServer(config)
	if err != nil {
		return
	}

	if err = server.Start(); err != nil {
		_ = server.Close()
		return
	}
	coreServer = server

	runtimeConfig = config
	debug.FreeOSMemory()
	return nil
}

// Run Xray instance with JSON configuration string.
// configJSON means the JSON configuration string.
func RunXrayFromJSON(configJSON string) (err error) {
	coreServerMu.Lock()
	defer coreServerMu.Unlock()
	if coreServer != nil {
		return ErrAlreadyRunning
	}
	memory.InitForceFree()
	config, err := loadConfigFromJSON(configJSON)
	if err != nil {
		return
	}

	server, err := startServer(config)
	if err != nil {
		return
	}

	if err = server.Start(); err != nil {
		_ = server.Close()
		return
	}
	coreServer = server
	runtimeConfig = config

	debug.FreeOSMemory()
	return nil
}

// Get Xray State
func GetXrayState() bool {
	coreServerMu.Lock()
	defer coreServerMu.Unlock()
	return coreServer != nil && coreServer.IsRunning()
}

// Stop Xray instance.
func StopXray() error {
	coreServerMu.Lock()
	defer coreServerMu.Unlock()
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
