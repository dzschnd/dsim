package topology

type File struct {
	Nodes []Node `json:"nodes"`
	Links []Link `json:"links"`
}

type Node struct {
	ID         string      `json:"id"`
	Type       string      `json:"type"`
	Position   Position    `json:"position"`
	Interfaces []Interface `json:"interfaces"`
	Routes     []Route     `json:"routes"`
	Running    bool        `json:"running"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Interface struct {
	Name string `json:"name"`
	CIDR string `json:"cidr,omitempty"`
}

type Route struct {
	Destination string `json:"destination"`
	NextHop     string `json:"nextHop"`
}

type Link struct {
	ID string       `json:"id"`
	A  LinkEndpoint `json:"a"`
	B  LinkEndpoint `json:"b"`
}

type LinkEndpoint struct {
	NodeID    string `json:"nodeId"`
	Interface string `json:"interface"`
}
