package jev

var ModelList = []string{
	"jev-latest",
	"jev-1.13.0",
	"drex-latest",
}

var ChannelName = "jev"

// contextKeyRequestJSON stashes the marshaled upstream request so DoResponse
// can synthesize prompt-token usage: upstream System One responses carry no
// usage of their own.
const contextKeyRequestJSON = "jev_request_json"
