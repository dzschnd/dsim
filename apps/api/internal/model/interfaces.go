package model

type TrafficConditions struct {
	DelayMs       int     `json:"delayMs"`
	JitterMs      int     `json:"jitterMs"`
	LossPct       float64 `json:"lossPct"`
	BandwidthKbit int     `json:"bandwidthKbit"`
}

type Interface struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	LinkID           string            `json:"linkId"`
	IPAddr           string            `json:"ipAddress"`
	PrefixLen        int               `json:"prefixLength"`
	RuntimeIPAddr    string            `json:"runtimeIpAddress"`
	RuntimePrefixLen int               `json:"runtimePrefixLength"`
	RuntimeName      string            `json:"runtimeName"`
	Conditions       TrafficConditions `json:"conditions"`
}
