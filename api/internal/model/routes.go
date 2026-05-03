package model

type RouteKind string

const (
	RouteKindVia       RouteKind = "via"
	RouteKindBlackhole RouteKind = "blackhole"
)

type Route struct {
	Destination string    `json:"destination"`
	NextHop     string    `json:"nextHop,omitempty"`
	Kind        RouteKind `json:"kind"`
}
