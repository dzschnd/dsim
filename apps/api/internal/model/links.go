package model

import "time"

type Link struct {
	ID          string    `json:"id"`
	NodeAID     string    `json:"nodeAId"`
	NodeBID     string    `json:"nodeBId"`
	NetworkID   string    `json:"networkId"`
	NetworkName string    `json:"networkName"`
	CreatedAt   time.Time `json:"createdAt"`
}
