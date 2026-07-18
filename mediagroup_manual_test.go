package main

import (
	"context"
	"testing"
	"time"
)

func TestManualMediaGroupDoesNotFlushOnTimer(t *testing.T) {
	b := &Bridge{mgBuffers: make(map[string]*mediaGroupBuffer)}
	b.bufferMediaGroupManual(context.Background(), "import-album", mediaGroupItem{})

	time.Sleep(mediaGroupTimeout + 100*time.Millisecond)

	b.mgMu.Lock()
	buf, ok := b.mgBuffers["import-album"]
	delete(b.mgBuffers, "import-album")
	b.mgMu.Unlock()
	if !ok {
		t.Fatal("manual media group was flushed before the importer completed it")
	}
	if buf.timer != nil {
		t.Fatal("manual media group unexpectedly has an auto-flush timer")
	}
}
