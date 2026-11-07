package bastion

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"sync"
)

/*
 * maxDecodedBody bounds what one console read may be once decompressed.
 *
 * Call asks the API server for gzip, so what crosses the tunnel is the
 * compressed answer — a pod list shrinks to a tenth of itself, which is the
 * difference between a dozen people on wide views fitting down one WebSocket and
 * not. The ceilings on the way (the agent's on what it reads, maxResponseBody on
 * what is joined here) count those compressed bytes, so none of them bounds what
 * the body becomes. This one does, and it is the agent's own 8 MiB: before Call
 * asked for gzip the agent's HTTP client decompressed transparently and that
 * ceiling counted exactly these bytes, so a read refused before is refused now.
 *
 * It is also the guard the compressed path needs: the tunnel is a trust
 * boundary, and a few kilobytes of gzip from an agent holding a leaked token can
 * expand into more memory than this server has.
 */
const maxDecodedBody = 8 << 20

var errDecodedTooLarge = fmt.Errorf(
	"the cluster's answer is larger than %d MiB once decompressed", maxDecodedBody>>20)

// gzipReaderPool holds *gzip.Reader values between calls so each console read
// does not pay a fresh allocation. Reset is cheaper than NewReader because it
// does not allocate a new header buffer.
var gzipReaderPool sync.Pool // *gzip.Reader

// decodeBody undoes the gzip the API server applied, in place, reading at most
// limit bytes of output. An encoding other than gzip is left as it arrived:
// gzip is the only one Call asks for, and the API server offers no other.
func decodeBody(resp *Response, limit int) error {
	if !strings.EqualFold(strings.TrimSpace(headerValue(resp.Header, "Content-Encoding")), "gzip") {
		return nil
	}

	src := bytes.NewReader(resp.Body)
	var reader *gzip.Reader
	if v := gzipReaderPool.Get(); v != nil {
		reader = v.(*gzip.Reader)
		if err := reader.Reset(src); err != nil {
			gzipReaderPool.Put(reader)
			return fmt.Errorf("the cluster's gzip answer could not be read: %w", err)
		}
	} else {
		var err error
		reader, err = gzip.NewReader(src)
		if err != nil {
			return fmt.Errorf("the cluster's gzip answer could not be read: %w", err)
		}
	}
	defer func() {
		_ = reader.Close()
		gzipReaderPool.Put(reader)
	}()

	// Pre-grow the buffer: gzip ratio is roughly 5–10x for JSON, so 4× the
	// compressed size is a cheap upper-bound seed that avoids most of the
	// doubling io.ReadAll would do from its 512-byte start.
	capacity := min(len(resp.Body)*4, limit)
	buf := bytes.NewBuffer(make([]byte, 0, capacity))
	if _, err := io.Copy(buf, io.LimitReader(reader, int64(limit)+1)); err != nil {
		return fmt.Errorf("the cluster's gzip answer could not be read: %w", err)
	}
	if buf.Len() > limit {
		return errDecodedTooLarge
	}

	resp.Body = buf.Bytes()
	deleteHeader(resp.Header, "Content-Encoding")
	deleteHeader(resp.Header, "Content-Length")
	return nil
}

// headerValue reads a header from the wire map, whose keys are canonical from a
// Go agent but are not promised to be.
func headerValue(header map[string][]string, name string) string {
	for key, values := range header {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func deleteHeader(header map[string][]string, name string) {
	for key := range header {
		if strings.EqualFold(key, name) {
			delete(header, key)
		}
	}
}
