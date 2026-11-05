module github.com/switchhosts/go-native

go 1.27.3

require github.com/egoist/mygo v0.7.4

// Pins the MyGo packaging CLI to the same version as the library, so CI can
// run `go tool mygo build` without installing anything else.
tool github.com/egoist/mygo/cmd/mygo

require (
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	golang.org/x/image v0.46.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
