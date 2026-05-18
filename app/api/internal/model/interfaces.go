package model

type TrafficConditions struct {
	DelayMs            int     `json:"delayMs"`
	JitterMs           int     `json:"jitterMs"`
	LossPct            float64 `json:"lossPct"`
	LossCorrelationPct float64 `json:"lossCorrelationPct"`
	ReorderPct         float64 `json:"reorderPct"`
	DuplicatePct       float64 `json:"duplicatePct"`
	CorruptPct         float64 `json:"corruptPct"`
	BandwidthKbit      int     `json:"bandwidthKbit"`
	QueueLimitPackets  int     `json:"queueLimitPackets"`
}

type InterfaceFlap struct {
	Enabled  bool `json:"enabled"`
	DownMs   int  `json:"downMs"`
	UpMs     int  `json:"upMs"`
	JitterMs int  `json:"jitterMs"`
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
	AdminDown        bool              `json:"adminDown"`
	Flap             InterfaceFlap     `json:"flap"`
}
