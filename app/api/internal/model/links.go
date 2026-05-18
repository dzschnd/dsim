package model

import "time"

type Link struct {
	ID           string    `json:"id"`
	InterfaceAID string    `json:"interfaceAId"`
	InterfaceBID string    `json:"interfaceBId"`
	Subnet       string    `json:"subnet"`
	CreatedAt    time.Time `json:"createdAt"`
}
