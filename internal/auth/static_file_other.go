//go:build !linux && !darwin

package auth

type staticFileStamp struct{ uid uint32 }

func readStaticPolicyFile(string) ([]byte, staticFileStamp, error) {
	return nil, staticFileStamp{}, refuse("unavailable")
}
