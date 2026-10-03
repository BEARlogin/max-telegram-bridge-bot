package main

import (
	"context"
	"testing"
	"time"
)

func mediaGroupReadyClosed(b *Bridge, buf *mediaGroupBuffer) bool {
	b.mgMu.Lock()
	defer b.mgMu.Unlock()
	return buf.readyClosed
}

// TestStaleMediaGroupTimerDoesNotReleaseNewBuffer: у нового буфера с тем же
// MediaGroupID поколение снова начинается с 1, поэтому одного номера поколения мало —
// старый таймер обязан сверить ещё и указатель на буфер.
func TestStaleMediaGroupTimerDoesNotReleaseNewBuffer(t *testing.T) {
	b := &Bridge{mgBuffers: make(map[string]*mediaGroupBuffer)}
	ctx := context.Background()

	b.bufferMediaGroup(ctx, "album", mediaGroupItem{})
	b.mgMu.Lock()
	stale := b.mgBuffers["album"]
	staleGeneration := stale.generation
	b.mgMu.Unlock()

	// Первый альбом ушёл явным сбросом — его задача очереди освобождается.
	if _, detached, err := b.detachMediaGroup("album", 0); err != nil || !detached {
		t.Fatalf("detach first album: detached=%v err=%v", detached, err)
	}
	if !mediaGroupReadyClosed(b, stale) {
		t.Fatal("явный сброс не освободил задачу очереди первого альбома")
	}

	// Новый альбом с тем же id: у него своё поколение, снова 1.
	b.bufferMediaGroup(ctx, "album", mediaGroupItem{caption: "подпись"})
	b.mgMu.Lock()
	fresh := b.mgBuffers["album"]
	if fresh.timer != nil {
		fresh.timer.Stop()
	}
	freshGeneration := fresh.generation
	b.mgMu.Unlock()
	if fresh == stale {
		t.Fatal("новый альбом должен быть отдельным буфером")
	}
	if freshGeneration != staleGeneration {
		t.Fatalf("поколения разошлись (%d и %d) — тест перестал проверять сверку указателя",
			freshGeneration, staleGeneration)
	}

	// Таймер старого буфера срабатывает поздно и с «правильным» поколением.
	b.signalMediaGroupReady("album", stale, staleGeneration)
	if mediaGroupReadyClosed(b, fresh) {
		t.Fatal("старый таймер отправил альбом, который ещё собирается")
	}

	// Убираем новый буфер, чтобы его задача очереди не осталась висеть.
	if _, detached, err := b.detachMediaGroup("album", 0); err != nil || !detached {
		t.Fatalf("detach fresh album: detached=%v err=%v", detached, err)
	}
}

// TestMediaGroupTimerReleasesItsOwnBuffer — обратный контроль к предыдущему тесту:
// у своего буфера таймер обязан сработать, иначе задача очереди никогда не заберёт
// альбом и чат встанет. Чат не связан нарочно: проверяем факт подхвата, а не доставку.
func TestMediaGroupTimerReleasesItsOwnBuffer(t *testing.T) {
	b := &Bridge{repo: testRepo(t), mgBuffers: make(map[string]*mediaGroupBuffer)}
	b.bufferMediaGroup(context.Background(), "album",
		mediaGroupItem{msg: &TGMessage{Chat: ChatInfo{ID: -1001}}})

	b.mgMu.Lock()
	buf := b.mgBuffers["album"]
	if buf.timer != nil {
		buf.timer.Stop()
	}
	generation := buf.generation
	b.mgMu.Unlock()

	b.signalMediaGroupReady("album", buf, generation)
	if !mediaGroupReadyClosed(b, buf) {
		t.Fatal("таймер своего буфера не отпустил альбом")
	}

	for deadline := time.Now().Add(5 * time.Second); ; {
		b.mgMu.Lock()
		_, still := b.mgBuffers["album"]
		b.mgMu.Unlock()
		if !still {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("задача очереди не забрала собранный альбом")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
