package xray

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"unsafe"

	xrayobservatory "github.com/xtls/xray-core/app/observatory"
	burstobservatory "github.com/xtls/xray-core/app/observatory/burst"
	xrayrouter "github.com/xtls/xray-core/app/router"
	extensionfeature "github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/features/routing"
)

type RuntimeDiagnostics struct {
	Running     bool                    `json:"running"`
	Balancers   []BalancerDiagnostics   `json:"balancers,omitempty"`
	Observatory *ObservatoryDiagnostics `json:"observatory,omitempty"`
}

type BalancerDiagnostics struct {
	Tag             string                       `json:"tag,omitempty"`
	Strategy        string                       `json:"strategy,omitempty"`
	Selectors       []string                     `json:"selectors,omitempty"`
	FallbackTag     string                       `json:"fallbackTag,omitempty"`
	OverrideTarget  string                       `json:"overrideTarget,omitempty"`
	PrincipleTarget []string                     `json:"principleTarget,omitempty"`
	CandidateCount  int                          `json:"candidateCount"`
	State           *BalancerStateDiagnostics    `json:"state,omitempty"`
	Settings        *BalancerSettingsDiagnostics `json:"settings,omitempty"`
	Error           string                       `json:"error,omitempty"`
}

type BalancerStateDiagnostics struct {
	LastIndex int `json:"lastIndex,omitempty"`
}

type BalancerSettingsDiagnostics struct {
	Expected   int32                          `json:"expected,omitempty"`
	MaxRTT     int64                          `json:"maxRTT,omitempty"`
	Tolerance  float32                        `json:"tolerance,omitempty"`
	Baselines  []int64                        `json:"baselines,omitempty"`
	Costs      []BalancerWeightDiagnostics    `json:"costs,omitempty"`
	HealthPing *ObservatoryHealthPingSettings `json:"healthPing,omitempty"`
}

type BalancerWeightDiagnostics struct {
	Match  string  `json:"match,omitempty"`
	Value  float64 `json:"value"`
	Regexp bool    `json:"regexp,omitempty"`
}

type ObservatoryDiagnostics struct {
	Type       string                          `json:"type,omitempty"`
	AliveCount int                             `json:"aliveCount"`
	TotalCount int                             `json:"totalCount"`
	Statuses   []OutboundStatusDiagnostics     `json:"statuses,omitempty"`
	Settings   *ObservatorySettingsDiagnostics `json:"settings,omitempty"`
	Error      string                          `json:"error,omitempty"`
}

type ObservatorySettingsDiagnostics struct {
	HealthPing *ObservatoryHealthPingSettings `json:"healthPing,omitempty"`
}

type ObservatoryHealthPingSettings struct {
	Destination   string `json:"destination,omitempty"`
	Connectivity  string `json:"connectivity,omitempty"`
	Interval      int64  `json:"interval,omitempty"`
	SamplingCount int32  `json:"samplingCount,omitempty"`
	Timeout       int64  `json:"timeout,omitempty"`
	HttpMethod    string `json:"httpMethod,omitempty"`
}

type OutboundStatusDiagnostics struct {
	OutboundTag     string                 `json:"outboundTag,omitempty"`
	Alive           bool                   `json:"alive"`
	Delay           int64                  `json:"delay"`
	LastErrorReason string                 `json:"lastErrorReason,omitempty"`
	LastSeenTime    int64                  `json:"lastSeenTime"`
	LastTryTime     int64                  `json:"lastTryTime"`
	HealthPing      *HealthPingDiagnostics `json:"healthPing,omitempty"`
}

type HealthPingDiagnostics struct {
	All       int64 `json:"all"`
	Fail      int64 `json:"fail"`
	Deviation int64 `json:"deviation"`
	Average   int64 `json:"average"`
	Max       int64 `json:"max"`
	Min       int64 `json:"min"`
}

func GetRuntimeDiagnostics(balancerTag string) (string, error) {
	diagnostics := RuntimeDiagnostics{
		Running: coreServer != nil && coreServer.IsRunning(),
	}

	if coreServer == nil {
		return marshalRuntimeDiagnostics(diagnostics)
	}

	diagnostics.Balancers = readBalancerDiagnostics(balancerTag)
	diagnostics.Observatory = readObservatoryDiagnostics()
	return marshalRuntimeDiagnostics(diagnostics)
}

func readBalancerDiagnostics(filterTag string) []BalancerDiagnostics {
	routerFeature, ok := coreServer.GetFeature(routing.RouterType()).(routing.Router)
	if !ok {
		if filterTag == "" {
			return nil
		}
		return []BalancerDiagnostics{{
			Tag:   filterTag,
			Error: "routing feature unavailable",
		}}
	}

	routerValue := reflect.ValueOf(routerFeature)
	if routerValue.Kind() != reflect.Ptr || routerValue.IsNil() {
		if filterTag == "" {
			return nil
		}
		return []BalancerDiagnostics{{
			Tag:   filterTag,
			Error: "router instance unavailable",
		}}
	}

	balancersField := exposeValue(routerValue.Elem().FieldByName("balancers"))
	if !balancersField.IsValid() || balancersField.Kind() != reflect.Map {
		if filterTag == "" {
			return nil
		}
		return []BalancerDiagnostics{{
			Tag:   filterTag,
			Error: "balancer registry unavailable",
		}}
	}

	keys := balancersField.MapKeys()
	tags := make([]string, 0, len(keys))
	for _, key := range keys {
		tag := key.String()
		if filterTag == "" || filterTag == tag {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)

	diagnostics := make([]BalancerDiagnostics, 0, len(tags))
	for _, tag := range tags {
		entry := BalancerDiagnostics{Tag: tag}
		balancerValue := balancersField.MapIndex(reflect.ValueOf(tag))
		if !balancerValue.IsValid() || balancerValue.IsNil() {
			entry.Error = "balancer unavailable"
			diagnostics = append(diagnostics, entry)
			continue
		}

		if err := fillBalancerDiagnostics(&entry, balancerValue, routerFeature); err != nil {
			entry.Error = err.Error()
		}
		diagnostics = append(diagnostics, entry)
	}

	if filterTag != "" && len(diagnostics) == 0 {
		return []BalancerDiagnostics{{
			Tag:   filterTag,
			Error: "balancer not found",
		}}
	}

	return diagnostics
}

func fillBalancerDiagnostics(diagnostics *BalancerDiagnostics, balancerValue reflect.Value, routerFeature routing.Router) error {
	balancerStruct := exposeValue(balancerValue.Elem())

	if selectorsField := exposeValue(balancerStruct.FieldByName("selectors")); selectorsField.IsValid() {
		diagnostics.Selectors = append([]string(nil), selectorsField.Interface().([]string)...)
	}

	if fallbackField := exposeValue(balancerStruct.FieldByName("fallbackTag")); fallbackField.IsValid() {
		diagnostics.FallbackTag = fallbackField.String()
	}

	if overrider, ok := routerFeature.(routing.BalancerOverrider); ok {
		overrideTarget, err := overrider.GetOverrideTarget(diagnostics.Tag)
		if err == nil {
			diagnostics.OverrideTarget = overrideTarget
		}
	}

	if principleTarget, ok := routerFeature.(routing.BalancerPrincipleTarget); ok {
		targets, err := principleTarget.GetPrincipleTarget(diagnostics.Tag)
		if err == nil {
			diagnostics.PrincipleTarget = append([]string(nil), targets...)
			diagnostics.CandidateCount = len(diagnostics.PrincipleTarget)
		}
	}

	strategyField := exposeValue(balancerStruct.FieldByName("strategy"))
	if !strategyField.IsValid() || strategyField.IsNil() {
		return nil
	}

	switch strategy := strategyField.Interface().(type) {
	case *xrayrouter.RandomStrategy:
		diagnostics.Strategy = "random"
	case *xrayrouter.RoundRobinStrategy:
		diagnostics.Strategy = "roundrobin"
		diagnostics.State = &BalancerStateDiagnostics{LastIndex: readRoundRobinIndex(strategy)}
	case *xrayrouter.LeastPingStrategy:
		diagnostics.Strategy = "leastping"
	case *xrayrouter.LeastLoadStrategy:
		diagnostics.Strategy = "leastload"
		diagnostics.Settings = readLeastLoadSettings(strategy)
	default:
		diagnostics.Strategy = reflect.TypeOf(strategy).String()
	}

	return nil
}

func readRoundRobinIndex(strategy *xrayrouter.RoundRobinStrategy) int {
	value := reflect.ValueOf(strategy)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return 0
	}

	indexField := exposeValue(value.Elem().FieldByName("index"))
	if !indexField.IsValid() {
		return 0
	}

	return int(indexField.Int())
}

func readLeastLoadSettings(strategy *xrayrouter.LeastLoadStrategy) *BalancerSettingsDiagnostics {
	value := reflect.ValueOf(strategy)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return nil
	}

	settingsField := exposeValue(value.Elem().FieldByName("settings"))
	if !settingsField.IsValid() || settingsField.IsNil() {
		return nil
	}

	settings, ok := settingsField.Interface().(*xrayrouter.StrategyLeastLoadConfig)
	if !ok || settings == nil {
		return nil
	}

	diagnostics := &BalancerSettingsDiagnostics{
		Expected:  settings.Expected,
		MaxRTT:    settings.MaxRTT,
		Tolerance: settings.Tolerance,
	}

	if len(settings.Baselines) > 0 {
		diagnostics.Baselines = append([]int64(nil), settings.Baselines...)
		sort.Slice(diagnostics.Baselines, func(i, j int) bool {
			return diagnostics.Baselines[i] < diagnostics.Baselines[j]
		})
	}

	if len(settings.Costs) > 0 {
		diagnostics.Costs = make([]BalancerWeightDiagnostics, 0, len(settings.Costs))
		for _, cost := range settings.Costs {
			if cost == nil {
				continue
			}
			diagnostics.Costs = append(diagnostics.Costs, BalancerWeightDiagnostics{
				Match:  cost.Match,
				Value:  float64(cost.Value),
				Regexp: cost.Regexp,
			})
		}
		sort.Slice(diagnostics.Costs, func(i, j int) bool {
			if diagnostics.Costs[i].Match != diagnostics.Costs[j].Match {
				return diagnostics.Costs[i].Match < diagnostics.Costs[j].Match
			}
			return diagnostics.Costs[i].Value < diagnostics.Costs[j].Value
		})
	}

	return diagnostics
}

func readObservatoryDiagnostics() *ObservatoryDiagnostics {
	observatoryFeature, ok := coreServer.GetFeature(extensionfeature.ObservatoryType()).(extensionfeature.Observatory)
	if !ok {
		return &ObservatoryDiagnostics{
			Error: "observatory feature unavailable",
		}
	}

	diagnostics := &ObservatoryDiagnostics{
		Type: observatoryTypeName(observatoryFeature),
	}

	if settings := readObservatorySettings(observatoryFeature); settings != nil {
		diagnostics.Settings = settings
	}

	observation, err := observatoryFeature.GetObservation(context.Background())
	if err != nil {
		diagnostics.Error = err.Error()
		return diagnostics
	}

	result, ok := observation.(*xrayobservatory.ObservationResult)
	if !ok {
		diagnostics.Error = "unexpected observatory payload"
		return diagnostics
	}

	statuses := make([]OutboundStatusDiagnostics, 0, len(result.Status))
	for _, status := range result.Status {
		if status == nil {
			continue
		}

		if status.Alive {
			diagnostics.AliveCount += 1
		}

		item := OutboundStatusDiagnostics{
			OutboundTag:     status.OutboundTag,
			Alive:           status.Alive,
			Delay:           status.Delay,
			LastErrorReason: status.LastErrorReason,
			LastSeenTime:    status.LastSeenTime,
			LastTryTime:     status.LastTryTime,
		}

		if status.HealthPing != nil {
			item.HealthPing = &HealthPingDiagnostics{
				All:       status.HealthPing.All,
				Fail:      status.HealthPing.Fail,
				Deviation: status.HealthPing.Deviation,
				Average:   status.HealthPing.Average,
				Max:       status.HealthPing.Max,
				Min:       status.HealthPing.Min,
			}
		}

		statuses = append(statuses, item)
	}

	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].OutboundTag < statuses[j].OutboundTag
	})

	diagnostics.Statuses = statuses
	diagnostics.TotalCount = len(statuses)
	return diagnostics
}

func observatoryTypeName(observatoryFeature extensionfeature.Observatory) string {
	switch observatoryFeature.(type) {
	case *xrayobservatory.Observer:
		return "observatory"
	case *burstobservatory.Observer:
		return "burst"
	default:
		typeName := reflect.TypeOf(observatoryFeature).String()
		typeName = strings.TrimPrefix(typeName, "*")
		return typeName
	}
}

func readObservatorySettings(observatoryFeature extensionfeature.Observatory) *ObservatorySettingsDiagnostics {
	switch observer := observatoryFeature.(type) {
	case *burstobservatory.Observer:
		return &ObservatorySettingsDiagnostics{
			HealthPing: readBurstHealthPingSettings(observer),
		}
	default:
		return nil
	}
}

func readBurstHealthPingSettings(observer *burstobservatory.Observer) *ObservatoryHealthPingSettings {
	value := reflect.ValueOf(observer)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return nil
	}

	healthCheckerField := exposeValue(value.Elem().FieldByName("hp"))
	if !healthCheckerField.IsValid() || healthCheckerField.IsNil() {
		return nil
	}

	healthPing, ok := healthCheckerField.Interface().(*burstobservatory.HealthPing)
	if !ok || healthPing == nil || healthPing.Settings == nil {
		return nil
	}

	return &ObservatoryHealthPingSettings{
		Destination:   healthPing.Settings.Destination,
		Connectivity:  healthPing.Settings.Connectivity,
		Interval:      int64(healthPing.Settings.Interval),
		SamplingCount: int32(healthPing.Settings.SamplingCount),
		Timeout:       int64(healthPing.Settings.Timeout),
		HttpMethod:    healthPing.Settings.HttpMethod,
	}
}

func exposeValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	if value.CanInterface() {
		return value
	}
	if value.CanAddr() {
		return reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem()
	}
	return value
}

func marshalRuntimeDiagnostics(diagnostics RuntimeDiagnostics) (string, error) {
	data, err := json.Marshal(diagnostics)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
