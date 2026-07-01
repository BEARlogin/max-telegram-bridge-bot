package main

import (
	"io"
	"log"
	"net/http"
)

// handleDebug — временный сбор launch-контекста мини-аппа, чтобы понять, КАК MAX
// передаёт параметры запуска и данные юзера (подпись/initData). Мини-апп шлёт сюда
// всё, что видит, при старте. Читаем в логе сервиса, после чего пишем верификацию.
func (s *server) handleDebug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	log.Printf("MINIAPP-DEBUG ua=%q\n%s", r.Header.Get("User-Agent"), string(body))
	w.WriteHeader(http.StatusNoContent)
}
