// Package version holds the version of this build.
package version

// Version is set at build time:
//
//	go build -ldflags "-X github.com/ZharfAI/zharf/version.Version=0.1.0"
var Version = "dev"
