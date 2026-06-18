package xray

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	xrayobservatory "github.com/xtls/xray-core/app/observatory"
	burstobservatory "github.com/xtls/xray-core/app/observatory/burst"
	approuter "github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	extensionfeature "github.com/xtls/xray-core/features/extension"
	featureoutbound "github.com/xtls/xray-core/features/outbound"
	featurerouting "github.com/xtls/xray-core/features/routing"
	"google.golang.org/protobuf/proto"
)

type ReplaceConfigResult struct {
	Running                 bool     `json:"running"`
	RemovedOutbounds        []string `json:"removedOutbounds,omitempty"`
	AddedOutbounds          []string `json:"addedOutbounds,omitempty"`
	ReusedOutbounds         []string `json:"reusedOutbounds,omitempty"`
	RoutingReloaded         bool     `json:"routingReloaded"`
	ObservatoryRestarted    bool     `json:"observatoryRestarted"`
	BurstObservatoryChecked bool     `json:"burstObservatoryChecked"`
}

func ReplaceConfig(datDir, configJSON string) (string, error) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	if coreServer == nil || !coreServer.IsRunning() {
		return "", errors.New("xray is not running")
	}

	if datDir != "" {
		InitEnv(datDir)
	}

	nextConfig, err := loadConfigFromJSON(configJSON)
	if err != nil {
		return "", err
	}

	result := ReplaceConfigResult{Running: true}

	oldOutbounds := outboundConfigMap(runtimeConfig)
	newOutbounds := outboundConfigMap(nextConfig)

	result.ReusedOutbounds = unchangedOutboundTags(oldOutbounds, newOutbounds)
	result.RemovedOutbounds = removedOutboundTags(oldOutbounds, newOutbounds)
	result.AddedOutbounds = addedOutboundTags(oldOutbounds, nextConfig)

	outboundManager, ok := coreServer.GetFeature(featureoutbound.ManagerType()).(featureoutbound.Manager)
	if !ok {
		return "", errors.New("outbound manager unavailable")
	}

	for _, tag := range result.RemovedOutbounds {
		if err := closeAndRemoveOutbound(outboundManager, tag); err != nil {
			return "", err
		}
	}

	for _, outboundConfig := range nextConfig.GetOutbound() {
		tag := outboundConfig.GetTag()
		if tag == "" || containsString(result.ReusedOutbounds, tag) {
			continue
		}

		if err := core.AddOutboundHandler(coreServer, proto.Clone(outboundConfig).(*core.OutboundHandlerConfig)); err != nil {
			return "", err
		}
	}

	if err := reloadRouting(nextConfig); err != nil {
		return "", err
	}
	result.RoutingReloaded = true

	restarted, burstChecked, err := restartObservatory(nextConfig)
	if err != nil {
		return "", err
	}
	result.ObservatoryRestarted = restarted
	result.BurstObservatoryChecked = burstChecked

	runtimeConfig = nextConfig
	payload, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func reloadRouting(config *core.Config) error {
	routerConfig := extractRouterConfig(config)
	if routerConfig == nil {
		return errors.New("router config missing from replacement config")
	}

	routerFeature, ok := coreServer.GetFeature(featurerouting.RouterType()).(featurerouting.Router)
	if !ok {
		return errors.New("routing feature unavailable")
	}

	return routerFeature.AddRule(serial.ToTypedMessage(proto.Clone(routerConfig).(*approuter.Config)), false)
}

func restartObservatory(config *core.Config) (bool, bool, error) {
	observatoryFeature, ok := coreServer.GetFeature(extensionfeature.ObservatoryType()).(extensionfeature.Observatory)
	if !ok || observatoryFeature == nil {
		return false, false, nil
	}

	switch observer := observatoryFeature.(type) {
	case *burstobservatory.Observer:
		burstConfig := extractBurstObservatoryConfig(config)
		if burstConfig == nil {
			return false, false, errors.New("burst observatory config missing from replacement config")
		}

		if err := observer.Close(); err != nil {
			return false, false, err
		}

		setStructField(observer, "config", proto.Clone(burstConfig))
		setStructField(observer, "hp", burstobservatory.NewHealthPing(observeContext(observer), observeDispatcher(), burstConfig.PingConfig))

		if err := observer.Start(); err != nil {
			return false, false, err
		}

		burstChecked := false
		if len(burstConfig.SubjectSelector) > 0 {
			if selector, ok := observeOutboundManager(observer).(featureoutbound.HandlerSelector); ok {
				tags := selector.Select(burstConfig.SubjectSelector)
				observer.Check(tags)
				burstChecked = true
			}
		}

		return true, burstChecked, nil
	case *xrayobservatory.Observer:
		observatoryConfig := extractObservatoryConfig(config)
		if observatoryConfig == nil {
			return false, false, errors.New("observatory config missing from replacement config")
		}

		if err := observer.Close(); err != nil {
			return false, false, err
		}

		setStructField(observer, "config", proto.Clone(observatoryConfig))
		setStructField(observer, "status", []*xrayobservatory.OutboundStatus(nil))

		if err := observer.Start(); err != nil {
			return false, false, err
		}

		return true, false, nil
	default:
		return false, false, nil
	}
}

func extractRouterConfig(config *core.Config) *approuter.Config {
	if config == nil {
		return nil
	}

	for _, typedMessage := range config.GetApp() {
		if typedMessage == nil {
			continue
		}

		message, err := typedMessage.GetInstance()
		if err != nil {
			continue
		}

		routerConfig, ok := message.(*approuter.Config)
		if ok {
			return routerConfig
		}
	}

	return nil
}

func extractObservatoryConfig(config *core.Config) *xrayobservatory.Config {
	for _, typedMessage := range collectFeatureMessages(config) {
		if typedMessage == nil {
			continue
		}

		message, err := typedMessage.GetInstance()
		if err != nil {
			continue
		}

		observatoryConfig, ok := message.(*xrayobservatory.Config)
		if ok {
			return observatoryConfig
		}
	}

	return nil
}

func extractBurstObservatoryConfig(config *core.Config) *burstobservatory.Config {
	for _, typedMessage := range collectFeatureMessages(config) {
		if typedMessage == nil {
			continue
		}

		message, err := typedMessage.GetInstance()
		if err != nil {
			continue
		}

		burstConfig, ok := message.(*burstobservatory.Config)
		if ok {
			return burstConfig
		}
	}

	return nil
}

func collectFeatureMessages(config *core.Config) []*serial.TypedMessage {
	if config == nil {
		return nil
	}

	messages := make([]*serial.TypedMessage, 0, len(config.GetApp())+len(config.GetExtension()))
	messages = append(messages, config.GetApp()...)
	messages = append(messages, config.GetExtension()...)
	return messages
}

func outboundConfigMap(config *core.Config) map[string]*core.OutboundHandlerConfig {
	if config == nil {
		return nil
	}

	result := make(map[string]*core.OutboundHandlerConfig)
	for _, outboundConfig := range config.GetOutbound() {
		if outboundConfig == nil || outboundConfig.GetTag() == "" {
			continue
		}
		result[outboundConfig.GetTag()] = outboundConfig
	}
	return result
}

func unchangedOutboundTags(oldConfigs, newConfigs map[string]*core.OutboundHandlerConfig) []string {
	var tags []string
	for tag, newConfig := range newConfigs {
		oldConfig, ok := oldConfigs[tag]
		if ok && proto.Equal(oldConfig, newConfig) {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

func removedOutboundTags(oldConfigs, newConfigs map[string]*core.OutboundHandlerConfig) []string {
	var tags []string
	for tag, oldConfig := range oldConfigs {
		newConfig, ok := newConfigs[tag]
		if !ok || !proto.Equal(oldConfig, newConfig) {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

func addedOutboundTags(oldConfigs map[string]*core.OutboundHandlerConfig, config *core.Config) []string {
	var tags []string
	if config == nil {
		return tags
	}

	for _, outboundConfig := range config.GetOutbound() {
		if outboundConfig == nil || outboundConfig.GetTag() == "" {
			continue
		}

		tag := outboundConfig.GetTag()
		oldConfig, ok := oldConfigs[tag]
		if !ok || !proto.Equal(oldConfig, outboundConfig) {
			tags = append(tags, tag)
		}
	}
	return tags
}

func closeAndRemoveOutbound(manager featureoutbound.Manager, tag string) error {
	if handler := manager.GetHandler(tag); handler != nil {
		if err := handler.Close(); err != nil {
			return err
		}
	}
	return manager.RemoveHandler(context.Background(), tag)
}

func observeDispatcher() featurerouting.Dispatcher {
	dispatcher, _ := coreServer.GetFeature(featurerouting.DispatcherType()).(featurerouting.Dispatcher)
	return dispatcher
}

func observeContext(observer any) context.Context {
	value := reflect.ValueOf(observer)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return context.Background()
	}

	ctxField := exposeValue(value.Elem().FieldByName("ctx"))
	if !ctxField.IsValid() || ctxField.IsNil() {
		return context.Background()
	}

	if ctx, ok := ctxField.Interface().(context.Context); ok && ctx != nil {
		return ctx
	}

	return context.Background()
}

func observeOutboundManager(observer any) featureoutbound.Manager {
	value := reflect.ValueOf(observer)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return nil
	}

	ohmField := exposeValue(value.Elem().FieldByName("ohm"))
	if !ohmField.IsValid() || ohmField.IsNil() {
		return nil
	}

	manager, _ := ohmField.Interface().(featureoutbound.Manager)
	return manager
}

func setStructField(target any, fieldName string, fieldValue any) {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return
	}

	field := exposeValue(value.Elem().FieldByName(fieldName))
	if !field.IsValid() || !field.CanSet() {
		return
	}

	newValue := reflect.ValueOf(fieldValue)
	if !newValue.IsValid() {
		field.Set(reflect.Zero(field.Type()))
		return
	}

	if newValue.Type().AssignableTo(field.Type()) {
		field.Set(newValue)
		return
	}

	if newValue.Type().ConvertibleTo(field.Type()) {
		field.Set(newValue.Convert(field.Type()))
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
