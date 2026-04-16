package model

import (
	"encoding/json"
	"fmt"
	"time"
)

type Node struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Status      NodeState   `json:"status"`
	Type        NodeType    `json:"type"`
	ContainerID string      `json:"containerId"`
	CreatedAt   time.Time   `json:"createdAt"`
	Interfaces  []Interface `json:"interfaces"`
	Routes      []Route     `json:"routes"`
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
	Error
)

var NodeStateName = map[NodeState]string{
	Idle:    "idle",
	Running: "running",
	Error:   "error",
}

func (t NodeState) MarshalJSON() ([]byte, error) {
	marshalled, ok := NodeStateName[t]
	if ok {
		return json.Marshal(marshalled)
	}
	return nil, fmt.Errorf("unknown node state: %d", t)
}
