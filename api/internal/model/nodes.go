package model

import (
	"encoding/json"
	"fmt"
	"time"
)

type Node struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Position    Position    `json:"position"`
	Status      NodeState   `json:"status"`
	Type        NodeType    `json:"type"`
	ContainerID string      `json:"-"`
	NetworkID   string      `json:"networkId"`
	NetworkName string      `json:"networkName"`
	Subnet      string      `json:"subnet"`
	CreatedAt   time.Time   `json:"createdAt"`
	Interfaces  []Interface `json:"interfaces"`
	Routes      []Route     `json:"routes"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type NodeType int

const (
	Host NodeType = iota
	Switch
	Router
)

var NameNodeType = map[string]NodeType{
	"host":   Host,
	"switch": Switch,
	"router": Router,
}

var NodeTypeName = map[NodeType]string{
	Host:   "host",
	Switch: "switch",
	Router: "router",
}

func (t NodeType) MarshalJSON() ([]byte, error) {
	marshalled, ok := NodeTypeName[t]
	if ok {
		return json.Marshal(marshalled)
	}
	return nil, fmt.Errorf("unknown node type: %d", t)
}

type NodeState int

const (
	Idle NodeState = iota
	Running
	Frozen
	Error
)

var NodeStateName = map[NodeState]string{
	Idle:    "idle",
	Running: "running",
	Frozen:  "frozen",
	Error:   "error",
}

func (t NodeState) MarshalJSON() ([]byte, error) {
	marshalled, ok := NodeStateName[t]
	if ok {
		return json.Marshal(marshalled)
	}
	return nil, fmt.Errorf("unknown node state: %d", t)
}
