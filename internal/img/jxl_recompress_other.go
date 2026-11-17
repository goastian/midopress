//go:build !((linux || darwin || freebsd) && (amd64 || arm64) && !nodynamic)

package img

func jxlNativeAvailable() bool { return false }

func jxlNativeVersion() string { return "" }

func jxlNativeEncodeJPEG([]byte) ([]byte, error) { return nil, ErrUnsupported }

func jxlNativeReconstructJPEG([]byte, int) ([]byte, error) { return nil, ErrUnsupported }
