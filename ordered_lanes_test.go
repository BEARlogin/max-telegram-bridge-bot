package main

import (
	"sync"
	"testing"
	"time"
)

// waitLaneSignal ждёт сигнал и валит тест по таймауту: зависшая очередь иначе
// выглядит как таймаут всего пакета, без указания на конкретный сценарий.
func waitLaneSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: сигнала нет за 5 с", what)
	}
}

func TestLaneDispatcherKeepsOrderWithinKey(t *testing.T) {
	d := newLaneDispatcher(nil)

	const jobs = 8
	var mu sync.Mutex
	var order []int
	done := make(chan struct{})

	for i := 0; i < jobs; i++ {
		// Первые задачи заметно дольше последних: без очереди быстрые обгонят их.
		pause := time.Duration(jobs-i) * 10 * time.Millisecond
		d.Submit("chat", func() {
			time.Sleep(pause)
			mu.Lock()
			order = append(order, i)
			last := len(order) == jobs
			mu.Unlock()
			if last {
				close(done)
			}
		})
	}

	waitLaneSignal(t, done, "очередь одного ключа")
	mu.Lock()
	defer mu.Unlock()
	for i, got := range order {
		if got != i {
			t.Fatalf("порядок в ключе нарушен: %v", order)
		}
	}
}

func TestLaneDispatcherRunsDifferentKeysInParallel(t *testing.T) {
	d := newLaneDispatcher(nil)

	first := make(chan struct{})
	second := make(chan struct{})

	// Задача первого ключа не завершится, пока не начнётся задача второго.
	// При общей очереди на все чаты тест встанет в дедлок и упадёт по таймауту.
	d.Submit(laneTgToMax(-100), func() {
		select {
		case <-second:
			close(first)
		case <-time.After(5 * time.Second):
		}
	})
	d.Submit(laneTgToMax(-200), func() { close(second) })

	waitLaneSignal(t, first, "параллельные ключи")
}

func TestLaneDispatcherWatchdogReleasesStalledLane(t *testing.T) {
	// Сроки сторожа диспетчер копирует при создании, поэтому короткие значения
	// ставим до конструктора и сразу возвращаем прежние.
	prevSlow, prevStall := laneJobSlowWarn, laneJobStallTimeout
	laneJobSlowWarn, laneJobStallTimeout = 20*time.Millisecond, 100*time.Millisecond
	d := newLaneDispatcher(nil)
	laneJobSlowWarn, laneJobStallTimeout = prevSlow, prevStall

	release := make(chan struct{})
	defer close(release)
	next := make(chan struct{})

	d.Submit("chat", func() { <-release }) // зависла насовсем
	d.Submit("chat", func() { close(next) })

	waitLaneSignal(t, next, "очередь после сторожа")
}

func TestLaneDispatcherSurvivesPanic(t *testing.T) {
	d := newLaneDispatcher(nil)
	next := make(chan struct{})

	d.Submit("chat", func() { panic("доставка упала") })
	d.Submit("chat", func() { close(next) })

	waitLaneSignal(t, next, "очередь после паники")
}

func TestNilLaneDispatcherRunsJob(t *testing.T) {
	var d *laneDispatcher
	done := make(chan struct{})
	d.Submit("chat", func() { close(done) })
	waitLaneSignal(t, done, "nil-диспетчер")
}

func TestUnorderedLaneDispatchLosesOrder(t *testing.T) {
	// Мутационный режим обязан быть действительно неупорядоченным: иначе
	// «мутант падает 5 из 5» ничего не доказывает.
	d := newLaneDispatcher(unorderedLaneDispatch)
	slowStarted := make(chan struct{})
	fastDone := make(chan struct{})

	d.Submit("chat", func() {
		close(slowStarted)
		time.Sleep(time.Second)
	})
	waitLaneSignal(t, slowStarted, "медленная задача")
	d.Submit("chat", func() { close(fastDone) })

	select {
	case <-fastDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("режим мутации всё-таки сохраняет порядок")
	}
}
