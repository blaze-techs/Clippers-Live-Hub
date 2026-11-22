package capture

import (
	"bufio"
	"context"
	"os"
	"time"
)

// tailList follows the segment muxer's live CSV and feeds finished segments
// into the ring. The file is append-only, so tracking a byte offset is enough.
func (e *Engine) tailList(ctx context.Context, listPath string, stop <-chan struct{}) {
	var offset int64
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
		}

		f, err := os.Open(listPath)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, 0); err != nil {
			f.Close()
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			offset += int64(len(line)) + 1
			if seg, ok := parseCSV(line, e.ring.dir); ok {
				e.ring.add(seg)
			}
		}
		f.Close()
		e.ring.Truncate()
	}
}
