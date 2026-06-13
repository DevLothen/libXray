package xray

import (
	"encoding/json"
	"reflect"
	"sort"

	featureoutbound "github.com/xtls/xray-core/features/outbound"
	featurerouting "github.com/xtls/xray-core/features/routing"
	featurestats "github.com/xtls/xray-core/features/stats"
)

type TrafficStats struct {
	Running  bool                     `json:"running"`
	Inbound  *TrafficCounterSnapshot  `json:"inbound,omitempty"`
	Balancer *BalancerTrafficSnapshot `json:"balancer,omitempty"`
}

type TrafficCounterSnapshot struct {
	Tag      string `json:"tag,omitempty"`
	Uplink   int64  `json:"uplink"`
	Downlink int64  `json:"downlink"`
}

type BalancerTrafficSnapshot struct {
	Tag             string                   `json:"tag,omitempty"`
	PrincipleTarget []string                 `json:"principleTarget,omitempty"`
	Uplink          int64                    `json:"uplink"`
	Downlink        int64                    `json:"downlink"`
	Outbounds       []TrafficCounterSnapshot `json:"outbounds,omitempty"`
}

func GetTrafficStats(balancerTag, inboundTag string) (string, error) {
	statsSnapshot := TrafficStats{
		Running: coreServer != nil && coreServer.IsRunning(),
	}

	if coreServer == nil {
		return marshalTrafficStats(statsSnapshot)
	}

	statsFeature, ok := coreServer.GetFeature(featurestats.ManagerType()).(featurestats.Manager)
	if !ok {
		return marshalTrafficStats(statsSnapshot)
	}

	if inboundTag != "" {
		statsSnapshot.Inbound = &TrafficCounterSnapshot{
			Tag:      inboundTag,
			Uplink:   counterValue(statsFeature, "inbound>>>"+inboundTag+">>>traffic>>>uplink"),
			Downlink: counterValue(statsFeature, "inbound>>>"+inboundTag+">>>traffic>>>downlink"),
		}
	}

	if balancerTag != "" {
		statsSnapshot.Balancer = readBalancerTrafficStats(statsFeature, balancerTag)
	}

	return marshalTrafficStats(statsSnapshot)
}

func readBalancerTrafficStats(statsFeature featurestats.Manager, balancerTag string) *BalancerTrafficSnapshot {
	snapshot := &BalancerTrafficSnapshot{Tag: balancerTag}
	if coreServer == nil {
		return snapshot
	}

	for _, target := range resolveBalancerOutboundTags(balancerTag) {
		outbound := TrafficCounterSnapshot{
			Tag:      target,
			Uplink:   counterValue(statsFeature, "outbound>>>"+target+">>>traffic>>>uplink"),
			Downlink: counterValue(statsFeature, "outbound>>>"+target+">>>traffic>>>downlink"),
		}
		snapshot.Uplink += outbound.Uplink
		snapshot.Downlink += outbound.Downlink
		snapshot.Outbounds = append(snapshot.Outbounds, outbound)
	}

	routerFeature, ok := coreServer.GetFeature(featurerouting.RouterType()).(featurerouting.Router)
	if !ok {
		return snapshot
	}

	principleTarget, ok := routerFeature.(featurerouting.BalancerPrincipleTarget)
	if !ok {
		return snapshot
	}

	targets, err := principleTarget.GetPrincipleTarget(balancerTag)
	if err != nil {
		return snapshot
	}

	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target == "" {
			continue
		}
		if _, exists := seen[target]; exists {
			continue
		}
		seen[target] = struct{}{}
		snapshot.PrincipleTarget = append(snapshot.PrincipleTarget, target)
	}
	sort.Strings(snapshot.PrincipleTarget)

	return snapshot
}

func resolveBalancerOutboundTags(balancerTag string) []string {
	routerFeature, ok := coreServer.GetFeature(featurerouting.RouterType()).(featurerouting.Router)
	if !ok {
		return nil
	}

	routerValue := reflect.ValueOf(routerFeature)
	if routerValue.Kind() != reflect.Ptr || routerValue.IsNil() {
		return nil
	}

	routerValue = exposeValue(routerValue.Elem())
	if !routerValue.IsValid() {
		return nil
	}

	balancersField := exposeValue(routerValue.FieldByName("balancers"))
	if !balancersField.IsValid() || balancersField.Kind() != reflect.Map {
		return nil
	}

	balancerValue := balancersField.MapIndex(reflect.ValueOf(balancerTag))
	if !balancerValue.IsValid() || balancerValue.IsNil() {
		return nil
	}

	balancerStruct := exposeValue(balancerValue.Elem())
	selectorsField := exposeValue(balancerStruct.FieldByName("selectors"))
	if !selectorsField.IsValid() {
		return nil
	}

	selectors, ok := selectorsField.Interface().([]string)
	if !ok || len(selectors) == 0 {
		return nil
	}

	outboundManager, ok := coreServer.GetFeature(featureoutbound.ManagerType()).(featureoutbound.Manager)
	if !ok {
		return nil
	}

	selector, ok := outboundManager.(featureoutbound.HandlerSelector)
	if !ok {
		return nil
	}

	tags := selector.Select(selectors)
	seen := make(map[string]struct{}, len(tags))
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	sort.Strings(result)
	return result
}

func counterValue(statsFeature featurestats.Manager, name string) int64 {
	counter := statsFeature.GetCounter(name)
	if counter == nil {
		return 0
	}
	return counter.Value()
}

func marshalTrafficStats(statsSnapshot TrafficStats) (string, error) {
	jsonBytes, err := json.Marshal(statsSnapshot)
	if err != nil {
		return "", err
	}
	return string(jsonBytes), nil
}
