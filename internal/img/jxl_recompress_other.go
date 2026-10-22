//go:build !((linux || darwin || freebsd) && (amd64 || arm64) && !nodynamic)

package img

func jxlNativeAvailable() bool { return false }

func jxlNativeEncodeJPEG([]byte) ([]byte, error) { return nil, ErrUnsupported }

func jxlNativeReconstructJPEG([]byte, int) ([]byte, error) { return nil, ErrUnsupported }
