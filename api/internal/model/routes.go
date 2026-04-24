package model

type Route struct {
	Destination string `json:"destination"`
	NextHop     string `json:"nextHop"`
}
