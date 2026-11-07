package dataprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

func streamError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ContextError(err)
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return NewError(StorageError)
}

// CopyResource streams a single caller-owned input to private staging. It reads
// no more than declared Size+1, verifies EOF/size/digest, never closes or retries
// the input, and uses constant payload working memory. Partial staging on any
// error must be discarded by its owner; nothing here commits provider state.
func CopyResource(ctx context.Context, dst io.Writer, in ResourceInput) (int64, error) {
	if err := ValidateResourceInfo(in.Info); err != nil {
		return 0, err
	}
	if in.Reader == nil || dst == nil {
		return 0, NewError(InvalidRequest)
	}
	h := sha256.New()
	buf := make([]byte, 32*1024)
	var count int64
	stalls := 0
	for {
		if err := ctx.Err(); err != nil {
			return count, ContextError(err)
		}
		want := in.Info.Size - count + 1
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		n, err := in.Reader.Read(buf[:int(want)])
		if n < 0 || n > int(want) {
			return count, NewError(StorageError)
		}
		if n > 0 {
			stalls = 0
			count += int64(n)
			if count > in.Info.Size {
				return count, NewError(InvalidSnapshot)
			}
			h.Write(buf[:n])
			written, we := dst.Write(buf[:n])
			if we != nil {
				return count, streamError(we)
			}
			if written != n {
				return count, NewError(StorageError)
			}
		} else {
			stalls++
			if stalls >= 100 {
				return count, NewError(StorageError)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return count, streamError(err)
			}
			if count != in.Info.Size || hex.EncodeToString(h.Sum(nil)) != in.Info.SHA256 {
				return count, NewError(InvalidSnapshot)
			}
			if err := ctx.Err(); err != nil {
				return count, ContextError(err)
			}
			return count, nil
		}
	}
}
