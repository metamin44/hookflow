package main

import "testing"

func TestMainPackageContract(t *testing.T) {
	// The previous blocked sentinel is intentionally gone: Owner approved D1a,
	// D1b, D2b-D2d and D3, and lifecycle tests exercise fail-closed startup.
	if defaultDependencies().loadPublisher == nil || defaultDependencies().loadRabbitMQ == nil {
		t.Fatal("publisher runtime dependencies are not wired")
	}
}
