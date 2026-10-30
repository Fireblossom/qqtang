package probe

import (
	"fmt"
	"path/filepath"
)

// Explicit shared_library_path values remain relative to the configuration
// file. An omitted value selects the release's library for the running binary.
func bundledONNXRuntimePath(goos, goarch string) (string, error) {
	var name string
	switch goos {
	case "windows":
		name = "onnxruntime.dll"
	case "linux":
		name = "libonnxruntime.so.1.29.0"
	case "darwin":
		// The official osx-arm64 1.29.0 archive's versioned library.
		name = "libonnxruntime.1.29.0.dylib"
	default:
		return "", fmt.Errorf("competitive_ai.shared_library_path is required for ONNX Runtime on %s/%s", goos, goarch)
	}
	return filepath.Join("..", "runtime", "onnxruntime", goos+"-"+goarch, name), nil
}
