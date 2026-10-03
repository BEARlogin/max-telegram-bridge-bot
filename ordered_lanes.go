package main

import (
	"log/slog"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

// Сторож задачи. Переменные, а не константы: тесту нужны короткие сроки.
var (
	// laneJobSlowWarn — через сколько задача считается подозрительно долгой.
	laneJobSlowWarn = 3 * time.Minute
	// laneJobStallTimeout — через сколько очередь идёт дальше, не дождавшись задачи.
	// Больше maxUploadTimeout для файлов (30 мин), поэтому нормальная загрузка
	// сторожа не задевает: он страхует от задачи, зависшей насовсем.
	laneJobStallTimeout = 35 * time.Minute
)

// lane — очередь задач одного ключа. Задачи ключа исполняются строго по одной,
// в порядке постановки; поле running говорит, что раннер ключа уже работает.
type lane struct {
	jobs    []func()
	running bool
}

// laneDispatcher раскладывает доставку по ключам: внутри ключа порядок получения
// сохраняется, разные ключи идут параллельно. Ключ — направление + чат назначения,
// так что медленное медиа одного чата не задерживает остальные.
type laneDispatcher struct {
	mu       sync.Mutex
	lanes    map[string]*lane
	dispatch func(job func())
	// Сроки сторожа фиксируются при создании: тест подменяет переменные пакета,
	// и копия в диспетчере убирает гонку с задачами, которые уже работают.
	slowAfter  time.Duration
	stallAfter time.Duration
}

// newLaneDispatcher создаёт диспетчер. dispatch != nil подменяет упорядоченное
// исполнение — это нужно только мутационному контролю тестов: в режиме
// «горутина на задачу» сценарный тест обязан упасть, иначе он ничего не проверяет.
func newLaneDispatcher(dispatch func(job func())) *laneDispatcher {
	return &laneDispatcher{
		lanes:      make(map[string]*lane),
		dispatch:   dispatch,
		slowAfter:  laneJobSlowWarn,
		stallAfter: laneJobStallTimeout,
	}
}

// unorderedLaneDispatch — режим мутационного контроля: каждая задача в своей горутине.
func unorderedLaneDispatch(job func()) { go job() }

func laneTgToMax(maxChatID int64) string {
	return "tg>max:" + strconv.FormatInt(maxChatID, 10)
}

func laneMaxToTg(tgChatID int64) string {
	return "max>tg:" + strconv.FormatInt(tgChatID, 10)
}

// Submit ставит задачу в очередь ключа. Задача-замыкание несёт ctx своего
// обновления: при дуал-боте в ctx лежит токен бота-источника, и подменять его
// общим ctx нельзя. Submit не блокирует вызывающего — очередь растёт в памяти.
func (d *laneDispatcher) Submit(key string, job func()) {
	if job == nil {
		return
	}
	// nil-диспетчер: фикстуры тестов собирают Bridge напрямую, без конструктора.
	// Прежнее поведение (горутина на сообщение) для них безопасно.
	if d == nil {
		go job()
		return
	}
	if d.dispatch != nil {
		d.dispatch(job)
		return
	}

	d.mu.Lock()
	ln, ok := d.lanes[key]
	if !ok {
		ln = &lane{}
		d.lanes[key] = ln
	}
	ln.jobs = append(ln.jobs, job)
	if !ln.running {
		ln.running = true
		go d.run(key, ln)
	}
	d.mu.Unlock()
}

// run — раннер одного ключа. Выход и удаление ключа происходят в одной критической
// секции с проверкой длины очереди: иначе Submit мог бы добавить задачу к раннеру,
// который уже решил выйти, и она осталась бы лежать навсегда.
func (d *laneDispatcher) run(key string, ln *lane) {
	for {
		d.mu.Lock()
		if len(ln.jobs) == 0 {
			ln.running = false
			delete(d.lanes, key)
			d.mu.Unlock()
			return
		}
		job := ln.jobs[0]
		ln.jobs[0] = nil
		ln.jobs = ln.jobs[1:]
		d.mu.Unlock()

		d.runJob(key, job)
	}
}

// runJob исполняет задачу под сторожем. Задача идёт в своей горутине, поэтому
// зависшая отправка не запирает ключ навсегда: через laneJobStallTimeout очередь
// продолжается (порядок в этом случае уже нарушен, но чат остаётся живым).
// recover стоит внутри горутины задачи — паника одного сообщения не роняет процесс
// и не останавливает очередь.
func (d *laneDispatcher) runJob(key string, job func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("lane job panicked", "lane", key, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		job()
	}()

	slow := time.NewTimer(d.slowAfter)
	defer slow.Stop()
	stall := time.NewTimer(d.stallAfter)
	defer stall.Stop()

	for {
		select {
		case <-done:
			return
		case <-slow.C:
			slog.Warn("lane job slow", "lane", key, "after", d.slowAfter)
		case <-stall.C:
			slog.Error("lane job stalled, continuing the queue", "lane", key, "after", d.stallAfter)
			return
		}
	}
}
