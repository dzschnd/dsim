package model

import "time"

type Link struct {
	ID           string    `json:"id"`
	InterfaceAID string    `json:"interfaceAId"`
	InterfaceBID string    `json:"interfaceBId"`
	NetworkID    string    `json:"networkId"`
	NetworkName  string    `json:"networkName"`
	CreatedAt    time.Time `json:"createdAt"`
}
