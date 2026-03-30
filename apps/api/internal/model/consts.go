package model

import (
	"encoding/json"
	"fmt"
)

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

func (t NodeType) String() string {
	switch t {
	case Host:
		return "host"
	case Switch:
		return "switch"
	case Router:
		return "router"
	default:
		return "unknown"
	}
}

func (t NodeType) MarshalJSON() ([]byte, error) {
	switch t {
	case Host, Switch, Router:
		return json.Marshal(t.String())
	default:
		return nil, fmt.Errorf("unknown node type: %d", t)
	}
}

type NodeState int

const (
	Idle NodeState = iota
	Running
	Error
)

func (s NodeState) String() string {
	switch s {
	case Idle:
		return "idle"
	case Running:
		return "running"
	case Error:
		return "error"
	default:
		return "unknown"
	}
}

func (s NodeState) MarshalJSON() ([]byte, error) {
	switch s {
	case Idle, Running, Error:
		return json.Marshal(s.String())
	default:
		return nil, fmt.Errorf("unknown node state: %d", s)
	}
}
