package model

type Interface struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	LinkID           string `json:"linkId"`
	IPAddr           string `json:"ipAddress"`
	PrefixLen        int    `json:"prefixLength"`
	RuntimeIPAddr    string `json:"runtimeIpAddress"`
	RuntimePrefixLen int    `json:"runtimePrefixLength"`
	RuntimeName      string `json:"runtimeName"`
}
